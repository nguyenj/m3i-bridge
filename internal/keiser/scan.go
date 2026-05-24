package keiser

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"tinygo.org/x/bluetooth"
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
	Adapter *bluetooth.Adapter // defaults to bluetooth.DefaultAdapter
	Logger  *slog.Logger       // defaults to slog.Default

	// Out is the channel parsed adverts are sent on. The scanner will not
	// block on a slow consumer — adverts get dropped (logged) if Out is full.
	// Caller owns construction (recommend buffer of ~16).
	Out chan<- Advert
}

// Run starts the scan and blocks. It returns when ctx is cancelled or the BLE
// stack fails to start. The scanner stops scanning cleanly on cancel.
func (s *Scanner) Run(ctx context.Context) error {
	adapter := s.Adapter
	if adapter == nil {
		adapter = bluetooth.DefaultAdapter
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	if s.Out == nil {
		return errors.New("keiser: Scanner.Out must be set")
	}

	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("enable BLE adapter: %w", err)
	}

	var filter DropoutFilter

	// Stop scanning when ctx is cancelled — this unblocks adapter.Scan.
	go func() {
		<-ctx.Done()
		if err := adapter.StopScan(); err != nil {
			log.Warn("stop scan", "err", err)
		}
	}()

	log.Info("BLE scan starting", "local_name", LocalName)
	err := adapter.Scan(func(_ *bluetooth.Adapter, sr bluetooth.ScanResult) {
		if sr.LocalName() != LocalName {
			return
		}
		raw, ok := extractKeiserPayload(sr.ManufacturerData())
		if !ok {
			return
		}
		advert, err := Parse(raw, time.Now())
		if err != nil {
			log.Debug("parse advert", "err", err, "raw_len", len(raw))
			return
		}
		advert = filter.Apply(advert)
		select {
		case s.Out <- advert:
		default:
			log.Warn("advert dropped: downstream channel full")
		}
	})

	// If ctx was cancelled, Scan returning nil is the expected outcome.
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("BLE scan: %w", err)
	}
	return nil
}

// extractKeiserPayload reconstructs the full 19-byte Keiser advert from the
// manufacturer data list returned by tinygo's BLE stack. Tinygo strips the
// 2-byte company ID prefix into a uint16 field and exposes the rest as Data,
// so we prepend the magic back to feed Parse the canonical buffer.
func extractKeiserPayload(elems []bluetooth.ManufacturerDataElement) ([]byte, bool) {
	for _, e := range elems {
		if e.CompanyID != CompanyID {
			continue
		}
		// e.Data is offsets 2..18 of the doc layout; prepend the magic.
		buf := make([]byte, 2+len(e.Data))
		binary.LittleEndian.PutUint16(buf[:2], CompanyID)
		copy(buf[2:], e.Data)
		return buf, true
	}
	return nil, false
}
