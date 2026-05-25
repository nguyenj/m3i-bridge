// Package broadcast drives the ANT USB stick. It is kept separate from
// internal/antplus so the pure page encoders can be built and tested without
// libusb-1.0-dev present on the host.
package broadcast

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"github.com/nguyenj/m3i-bridge/internal/antplus"
	"github.com/nguyenj/m3i-bridge/internal/session"
)

// Hardcoded ANT+ channel parameters for the bridge. They are intentionally
// not exposed as flags — the FR955 pairs the device number once and any
// change would force re-pairing.
const (
	powerChannelNumber uint8  = 0
	speedChannelNumber uint8  = 1
	networkNumber      uint8  = 0
	powerDeviceNumber  uint16 = 0x52E1 // arbitrary, must be non-zero
	speedDeviceNumber  uint16 = 0x52E2 // separate Bike Speed sensor identity
	powerTransmission  uint8  = 0x05   // Bicycle Power uses global data pages.
	speedTransmission  uint8  = 0x01   // Bike Speed does not use global data pages.

	powerModelNumber uint16 = 1
	softwareRevision uint8  = 1

	powerResponseRepeats = 4
)

// Broadcaster owns the ANT USB stick and turns session events into ANT+ Power
// Meter and Bike Speed broadcasts. Run blocks until the supplied context is
// cancelled or the underlying USB stack returns an error.
type Broadcaster struct {
	OpenANT antOpenFunc // defaults to the built-in ANT USB transport
	Logger  *slog.Logger

	// Events is the channel of FSM events the broadcaster consumes. Caller
	// owns construction.
	Events <-chan session.Event
}

// Run drives the broadcaster until ctx is cancelled.
func (b *Broadcaster) Run(ctx context.Context) error {
	// Pin to a single OS thread — ANT+ master channels are timing-sensitive
	// and scheduler jitter can cause missed slots. Cheap insurance on a
	// quad-core Pi Zero 2 W.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	log := b.Logger
	if log == nil {
		log = slog.Default()
	}
	if b.Events == nil {
		return errors.New("antplus: Broadcaster.Events must be set")
	}

	openANT := b.OpenANT
	if openANT == nil {
		openANT = openANTUSB
	}

	device, err := openANT(ctx, log)
	if err != nil {
		return fmt.Errorf("ant start: %w", err)
	}
	defer func() {
		if err := device.Close(); err != nil {
			log.Warn("ant close failed", "err", err)
		}
	}()

	// One-time chip setup.
	if err := device.ResetSystem(ctx); err != nil {
		return fmt.Errorf("ant reset: %w", err)
	}
	if err := device.SetNetworkKey(ctx, networkNumber, antPlusNetworkKey); err != nil {
		return fmt.Errorf("ant set network key: %w", err)
	}

	log.Info("ant+ broadcaster ready",
		"power_device_number", powerDeviceNumber,
		"power_device_type", antplus.PowerDeviceType,
		"speed_device_number", speedDeviceNumber,
		"speed_device_type", antplus.SpeedDeviceType,
		"speed_wheel_mm", antplus.VirtualWheelCircumferenceMM,
		"rf_freq", antplus.RFFrequency,
		"power_period", antplus.PowerChannelPeriod,
		"speed_period", antplus.SpeedChannelPeriod,
		"power_transmission_type", powerTransmission,
		"speed_transmission_type", speedTransmission)

	state := broadcasterState{log: log, dev: device}
	if err := state.startSession(ctx); err != nil {
		return err
	}
	defer state.endSession(context.Background()) // ensure clean shutdown if context cancelled
	if err := state.broadcastAll(ctx); err != nil {
		return err
	}

	refreshTicker := time.NewTicker(time.Second)
	defer refreshTicker.Stop()
	summaryTicker := time.NewTicker(30 * time.Second)
	defer summaryTicker.Stop()
	channelEvents := device.ChannelEvents()
	dataMessages := device.DataMessages()

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-b.Events:
			if !ok {
				return nil
			}
			if err := state.handle(ctx, ev); err != nil {
				return err
			}
		case ev := <-channelEvents:
			if err := state.handleANTEvent(ctx, ev); err != nil {
				return err
			}
		case msg := <-dataMessages:
			state.handleANTData(msg)
		case <-refreshTicker.C:
			state.refreshBroadcasts++
			if err := state.broadcastAll(ctx); err != nil {
				return err
			}
		case <-summaryTicker.C:
			state.logSummary()
		}
	}
}

// broadcasterState is the per-process state of the running broadcaster. It is
// only touched from Run's goroutine, so no synchronization is needed.
type broadcasterState struct {
	log *slog.Logger
	dev antController

	powerEncoder antplus.PowerEncoder
	speedEncoder antplus.SpeedEncoder
	active       bool
	power        uint16
	cadence      uint16
	pendingPower [][8]byte

	channelEvents      uint64
	txEvents           uint64
	rxMessages         uint64
	calibrationReqs    uint64
	pageRequests       uint64
	responseBroadcasts uint64
	refreshBroadcasts  uint64
	powerBroadcasts    uint64
	speedBroadcasts    uint64
	commonBroadcasts   uint64
	nonZeroBroadcasts  uint64
}

func (s *broadcasterState) handle(ctx context.Context, ev session.Event) error {
	switch ev.Type {
	case session.EventSessionStarted:
		if err := s.startSession(ctx); err != nil {
			return err
		}
		s.applyStats(ev)
	case session.EventStatsUpdated:
		s.applyStats(ev)
	case session.EventSessionStale:
		// FSM already zeros power/cadence and emits a StatsUpdated alongside.
		// Nothing extra to do here.
	case session.EventSessionEnded:
		s.power, s.cadence = 0, 0
	}
	return nil
}

func (s *broadcasterState) applyStats(ev session.Event) {
	s.power = ev.Power
	s.cadence = ev.Cadence
	if ev.DistanceValid {
		s.speedEncoder.ObserveDistance(ev.At, ev.DistanceTenths, ev.DistanceMetric)
	}
}

func (s *broadcasterState) handleANTData(msg antDataMessage) {
	s.rxMessages++
	page := msg.data[0]
	s.log.Info("ant rx data",
		"channel", msg.channel,
		"acknowledged", msg.acknowledged,
		"page", fmt.Sprintf("0x%02x", page),
		"data", fmt.Sprintf("% x", msg.data))

	if msg.channel != powerChannelNumber {
		return
	}

	switch page {
	case antplus.PowerPageCalibration:
		if msg.data[1] == antplus.CalibrationRequest {
			s.calibrationReqs++
			s.queuePowerResponse(antplus.EncodeCalibrationResponse(), powerResponseRepeats)
			s.log.Info("queued power calibration response", "repeats", powerResponseRepeats)
		}
	case antplus.CommonPageRequest:
		s.handlePowerPageRequest(msg.data)
	}
}

func (s *broadcasterState) handlePowerPageRequest(data [8]byte) {
	requestedPage := data[6]
	switch requestedPage {
	case antplus.CommonPageManufacturer:
		s.pageRequests++
		s.queuePowerResponse(antplus.EncodeCommonPage80(powerModelNumber), responseRepeats(data[5]))
	case antplus.CommonPageProduct:
		s.pageRequests++
		s.queuePowerResponse(antplus.EncodeCommonPage81(softwareRevision, uint32(powerDeviceNumber)), responseRepeats(data[5]))
	case antplus.PowerPageStandard:
		s.pageRequests++
		s.queuePowerResponse(s.powerEncoder.EncodePage10(s.power, s.cadence), responseRepeats(data[5]))
	default:
		s.log.Info("ignoring unsupported ANT+ page request", "requested_page", fmt.Sprintf("0x%02x", requestedPage))
	}
}

func (s *broadcasterState) handleANTEvent(ctx context.Context, ev antEvent) error {
	s.channelEvents++
	if ev.code != eventTx {
		return nil
	}
	s.txEvents++
	switch ev.channel {
	case powerChannelNumber:
		return s.broadcastPower(ctx)
	case speedChannelNumber:
		return s.broadcastSpeed(ctx)
	default:
		return nil
	}
}

func (s *broadcasterState) startSession(ctx context.Context) error {
	if s.active {
		return nil
	}
	s.log.Info("ant+ opening power-meter and bike-speed channels")
	s.powerEncoder = antplus.PowerEncoder{}
	s.pendingPower = nil
	if err := s.openChannel(ctx, powerChannelNumber, powerDeviceNumber, antplus.PowerDeviceType, powerTransmission, antplus.PowerChannelPeriod); err != nil {
		return err
	}
	if err := s.openChannel(ctx, speedChannelNumber, speedDeviceNumber, antplus.SpeedDeviceType, speedTransmission, antplus.SpeedChannelPeriod); err != nil {
		return err
	}
	s.active = true
	return nil
}

func (s *broadcasterState) openChannel(ctx context.Context, channel uint8, deviceNumber uint16, deviceType uint8, transmissionType uint8, period uint16) error {
	if err := s.dev.AssignChannel(ctx, channel, channelTypeTransmit, networkNumber); err != nil {
		return fmt.Errorf("ant assign channel %d: %w", channel, err)
	}
	if err := s.dev.SetChannelID(ctx, channel, deviceNumber, deviceType, transmissionType); err != nil {
		return fmt.Errorf("ant set channel id %d: %w", channel, err)
	}
	if err := s.dev.SetChannelRFFrequency(ctx, channel, antplus.RFFrequency); err != nil {
		return fmt.Errorf("ant set channel rf %d: %w", channel, err)
	}
	if err := s.dev.SetChannelTransmitPower(ctx, channel, radioTransmitPowerMax); err != nil {
		s.log.Warn("ant per-channel transmit power not accepted", "channel", channel, "err", err)
	}
	if err := s.dev.SetChannelPeriod(ctx, channel, period); err != nil {
		return fmt.Errorf("ant set channel period %d: %w", channel, err)
	}
	if err := s.dev.OpenChannel(ctx, channel); err != nil {
		return fmt.Errorf("ant open channel %d: %w", channel, err)
	}
	s.log.Info("ant channel opened", "channel", channel, "device_number", deviceNumber, "device_type", deviceType, "transmission_type", transmissionType, "period", period)
	return nil
}

func (s *broadcasterState) endSession(ctx context.Context) {
	if !s.active {
		return
	}
	s.log.Info("ant+ closing power-meter and bike-speed channels")
	if err := s.dev.CloseChannel(ctx, speedChannelNumber); err != nil {
		s.log.Warn("ant close speed channel failed", "err", err)
	}
	if err := s.dev.UnassignChannel(ctx, speedChannelNumber); err != nil {
		s.log.Warn("ant unassign speed channel failed", "err", err)
	}
	if err := s.dev.CloseChannel(ctx, powerChannelNumber); err != nil {
		s.log.Warn("ant close power channel failed", "err", err)
	}
	if err := s.dev.UnassignChannel(ctx, powerChannelNumber); err != nil {
		s.log.Warn("ant unassign power channel failed", "err", err)
	}
	s.active = false
	s.power, s.cadence = 0, 0
	s.pendingPower = nil
}

func (s *broadcasterState) broadcastAll(ctx context.Context) error {
	if !s.active {
		return nil
	}
	if err := s.broadcastPower(ctx); err != nil {
		return err
	}
	return s.broadcastSpeed(ctx)
}

func (s *broadcasterState) broadcastPower(ctx context.Context) error {
	if !s.active {
		return nil
	}
	powerPage := s.nextPowerPage()
	if err := s.dev.SendBroadcastData(ctx, powerChannelNumber, powerPage[:]); err != nil {
		return fmt.Errorf("ant broadcast power: %w", err)
	}
	s.powerBroadcasts++
	if isCommonPage(powerPage) {
		s.commonBroadcasts++
	} else if powerPage[0] == antplus.PowerPageCalibration {
		s.responseBroadcasts++
	} else if s.power > 0 || s.cadence > 0 {
		s.nonZeroBroadcasts++
	}
	return nil
}

func (s *broadcasterState) broadcastSpeed(ctx context.Context) error {
	if !s.active {
		return nil
	}
	speedPage := s.nextSpeedPage()
	if err := s.dev.SendBroadcastData(ctx, speedChannelNumber, speedPage[:]); err != nil {
		return fmt.Errorf("ant broadcast speed: %w", err)
	}
	s.speedBroadcasts++
	return nil
}

func (s *broadcasterState) nextPowerPage() [8]byte {
	if len(s.pendingPower) > 0 {
		page := s.pendingPower[0]
		copy(s.pendingPower, s.pendingPower[1:])
		s.pendingPower = s.pendingPower[:len(s.pendingPower)-1]
		return page
	}
	if page, ok := antplus.CommonInterleavedPage(s.powerBroadcasts+1, powerModelNumber, softwareRevision, uint32(powerDeviceNumber)); ok {
		return page
	}
	return s.powerEncoder.EncodePage10(s.power, s.cadence)
}

func (s *broadcasterState) nextSpeedPage() [8]byte {
	return s.speedEncoder.EncodePage0()
}

func (s *broadcasterState) queuePowerResponse(page [8]byte, repeats int) {
	for range repeats {
		s.pendingPower = append(s.pendingPower, page)
	}
	if len(s.pendingPower) > 16 {
		s.pendingPower = s.pendingPower[len(s.pendingPower)-16:]
	}
}

func responseRepeats(requestedTransmission byte) int {
	repeats := int(requestedTransmission & 0x7F)
	if repeats <= 0 || repeats > powerResponseRepeats {
		return powerResponseRepeats
	}
	return repeats
}

func isCommonPage(page [8]byte) bool {
	return page[0] == antplus.CommonPageManufacturer || page[0] == antplus.CommonPageProduct
}

func (s *broadcasterState) logSummary() {
	s.log.Info("ant broadcast summary",
		"active", s.active,
		"power", s.power,
		"cadence", s.cadence,
		"channel_events", s.channelEvents,
		"tx_events", s.txEvents,
		"rx_messages", s.rxMessages,
		"calibration_requests", s.calibrationReqs,
		"page_requests", s.pageRequests,
		"response_broadcasts", s.responseBroadcasts,
		"refresh_broadcasts", s.refreshBroadcasts,
		"power_broadcasts", s.powerBroadcasts,
		"speed_broadcasts", s.speedBroadcasts,
		"common_broadcasts", s.commonBroadcasts,
		"non_zero_broadcasts", s.nonZeroBroadcasts)
}
