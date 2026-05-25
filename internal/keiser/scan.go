package keiser

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// LocalName advertised by Keiser M Series bikes.
const LocalName = "M3"

// CompanyID is the value that 0x02 0x01 (LE) decodes to. Tinygo's BLE stack
// parses the first two bytes of manufacturer data as the company ID and
// returns the rest in Data; we use this constant to match incoming adverts.
const CompanyID uint16 = 0x0102

// Scanner watches BLE adverts and emits parsed, dropout-filtered Adverts on
// its Out channel.
//
// Run blocks until the supplied context is cancelled or the underlying BLE
// stack returns an error. Cancellation triggers StopScan and a clean shutdown.
type Scanner struct {
	Logger *slog.Logger // defaults to slog.Default

	// Out is the channel parsed adverts are sent on. The scanner will not
	// block on a slow consumer — adverts get dropped (logged) if Out is full.
	// Caller owns construction (recommend buffer of ~16).
	Out chan<- Advert
}

// Run starts the scan and blocks. It returns when ctx is cancelled or the BLE
// stack fails to start. The scanner stops scanning cleanly on cancel.
func (s *Scanner) Run(ctx context.Context) error {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	if s.Out == nil {
		return errors.New("keiser: Scanner.Out must be set")
	}

	bus, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("connect system D-Bus: %w", err)
	}
	defer bus.Close()

	bluez := bus.Object("org.bluez", dbus.ObjectPath("/"))
	adapter := bus.Object("org.bluez", dbus.ObjectPath("/org/bluez/hci0"))

	powered, err := adapter.GetProperty("org.bluez.Adapter1.Powered")
	if err != nil {
		return fmt.Errorf("read hci0 power state: %w", err)
	}
	if on, ok := powered.Value().(bool); !ok || !on {
		return errors.New("bluetooth adapter hci0 is not powered")
	}

	var filter DropoutFilter
	stats := scanStats{started: time.Now()}

	signal := make(chan *dbus.Signal, 64)
	bus.Signal(signal)
	defer bus.RemoveSignal(signal)

	propertiesChangedMatchOptions := []dbus.MatchOption{
		dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez")),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
	}
	if err := bus.AddMatchSignal(propertiesChangedMatchOptions...); err != nil {
		return fmt.Errorf("subscribe to BlueZ properties: %w", err)
	}
	defer bus.RemoveMatchSignal(propertiesChangedMatchOptions...)

	objectManagerMatchOptions := []dbus.MatchOption{
		dbus.WithMatchPathNamespace(dbus.ObjectPath("/org/bluez")),
		dbus.WithMatchInterface("org.freedesktop.DBus.ObjectManager"),
	}
	if err := bus.AddMatchSignal(objectManagerMatchOptions...); err != nil {
		return fmt.Errorf("subscribe to BlueZ object manager: %w", err)
	}
	defer bus.RemoveMatchSignal(objectManagerMatchOptions...)

	if call := adapter.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0, map[string]interface{}{
		"Transport":     "le",
		"DuplicateData": true,
	}); call.Err != nil {
		return fmt.Errorf("set BLE discovery filter: %w", call.Err)
	}
	defer adapter.Call("org.bluez.Adapter1.SetDiscoveryFilter", 0)

	devices, err := managedDevices(bluez, adapter.Path())
	if err != nil {
		return err
	}
	for _, props := range devices {
		s.processDevice(props, &filter, &stats)
	}

	startDiscovery := adapter.Go("org.bluez.Adapter1.StartDiscovery", 0, nil)
	startDiscoveryDone := startDiscovery.Done
	defer adapter.Call("org.bluez.Adapter1.StopDiscovery", 0)

	log.Info("BLE scan starting", "local_name", LocalName, "duplicate_data", true)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-startDiscoveryDone:
			if startDiscovery.Err != nil {
				return fmt.Errorf("start BLE discovery: %w", startDiscovery.Err)
			}
			startDiscoveryDone = nil
		case sig := <-signal:
			switch sig.Name {
			case "org.freedesktop.DBus.ObjectManager.InterfacesAdded":
				objectPath := sig.Body[0].(dbus.ObjectPath)
				if !strings.HasPrefix(string(objectPath), string(adapter.Path())) {
					continue
				}
				interfaces, ok := sig.Body[1].(map[string]map[string]dbus.Variant)
				if !ok {
					continue
				}
				props, ok := interfaces["org.bluez.Device1"]
				if !ok {
					continue
				}
				devices[objectPath] = props
				s.processDevice(props, &filter, &stats)
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				iface, _ := sig.Body[0].(string)
				if iface == "org.bluez.Adapter1" {
					changes, _ := sig.Body[1].(map[string]dbus.Variant)
					if discovering, ok := boolProperty(changes, "Discovering"); ok && !discovering {
						return errors.New("BLE discovery stopped unexpectedly")
					}
					continue
				}
				if iface != "org.bluez.Device1" {
					continue
				}
				props, ok := devices[sig.Path]
				if !ok {
					continue
				}
				changes, _ := sig.Body[1].(map[string]dbus.Variant)
				for k, v := range changes {
					props[k] = v
				}
				s.processDevice(props, &filter, &stats)
			}
		case <-ticker.C:
			stats.log(log)
		}
	}
}

func (s *Scanner) processDevice(props map[string]dbus.Variant, filter *DropoutFilter, stats *scanStats) {
	stats.observed++
	localName, _ := stringProperty(props, "Name")
	raw, ok := extractKeiserPayload(manufacturerData(props))
	if !ok {
		return
	}
	stats.payloads++
	advert, err := Parse(raw, time.Now())
	if err != nil {
		stats.parseErrors++
		return
	}
	if !advert.DataType.Classify().IsRealtime() {
		stats.reviewOrUnknown++
		return
	}

	advert = filter.Apply(advert)
	stats.realtime++
	stats.lastName = localName
	stats.lastPower = advert.PowerWatts
	stats.lastCadence = advert.CadenceRPM
	stats.lastDistanceTenths = advert.DistanceTenths
	stats.lastDistanceMetric = advert.DistanceMetric
	stats.lastRealtime = advert.Received

	select {
	case s.Out <- advert:
	default:
		stats.dropped++
		if s.Logger != nil {
			s.Logger.Warn("advert dropped: downstream channel full")
		}
	}
}

// extractKeiserPayload reconstructs the full 19-byte Keiser advert from the
// manufacturer data map. BlueZ separates the first two manufacturer-data bytes
// into a company ID, so we prepend the magic back to feed Parse the canonical
// buffer.
func extractKeiserPayload(elems map[uint16][]byte) ([]byte, bool) {
	for companyID, data := range elems {
		if companyID != CompanyID {
			continue
		}
		// data is offsets 2..18 of the doc layout; prepend the magic.
		buf := make([]byte, 2+len(data))
		binary.LittleEndian.PutUint16(buf[:2], CompanyID)
		copy(buf[2:], data)
		return buf, true
	}
	return nil, false
}

func managedDevices(bluez dbus.BusObject, adapterPath dbus.ObjectPath) (map[dbus.ObjectPath]map[string]dbus.Variant, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := bluez.Call("org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return nil, fmt.Errorf("read BlueZ managed objects: %w", err)
	}

	devices := make(map[dbus.ObjectPath]map[string]dbus.Variant)
	for path, interfaces := range objects {
		if !strings.HasPrefix(string(path), string(adapterPath)) {
			continue
		}
		if props, ok := interfaces["org.bluez.Device1"]; ok {
			devices[path] = props
		}
	}
	return devices, nil
}

func manufacturerData(props map[string]dbus.Variant) map[uint16][]byte {
	out := make(map[uint16][]byte)
	raw, ok := props["ManufacturerData"]
	if !ok {
		return out
	}
	values, ok := raw.Value().(map[uint16]dbus.Variant)
	if ok {
		for id, variant := range values {
			data, ok := variant.Value().([]byte)
			if !ok {
				continue
			}
			out[id] = data
		}
		return out
	}
	byteValues, ok := raw.Value().(map[uint16][]byte)
	if ok {
		for id, data := range byteValues {
			out[id] = data
		}
	}
	return out
}

func stringProperty(props map[string]dbus.Variant, name string) (string, bool) {
	raw, ok := props[name]
	if !ok {
		return "", false
	}
	value, ok := raw.Value().(string)
	return value, ok
}

func boolProperty(props map[string]dbus.Variant, name string) (bool, bool) {
	raw, ok := props[name]
	if !ok {
		return false, false
	}
	value, ok := raw.Value().(bool)
	return value, ok
}

type scanStats struct {
	started time.Time

	observed        uint64
	payloads        uint64
	realtime        uint64
	reviewOrUnknown uint64
	parseErrors     uint64
	dropped         uint64

	lastName           string
	lastRealtime       time.Time
	lastPower          uint16
	lastCadence        uint16
	lastDistanceTenths uint16
	lastDistanceMetric bool
}

func (s *scanStats) log(log *slog.Logger) {
	attrs := []any{
		"uptime", time.Since(s.started).Round(time.Second),
		"observed", s.observed,
		"keiser_payloads", s.payloads,
		"realtime", s.realtime,
		"review_or_unknown", s.reviewOrUnknown,
		"parse_errors", s.parseErrors,
		"dropped", s.dropped,
	}
	if !s.lastRealtime.IsZero() {
		attrs = append(attrs,
			"last_seen_ago", time.Since(s.lastRealtime).Round(time.Second),
			"last_name", s.lastName,
			"last_power", s.lastPower,
			"last_cadence", s.lastCadence,
			"last_distance_tenths", s.lastDistanceTenths,
			"last_distance_metric", s.lastDistanceMetric)
	}
	log.Info("BLE scan summary", attrs...)
}
