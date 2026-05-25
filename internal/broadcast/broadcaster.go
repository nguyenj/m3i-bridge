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
	transmissionType   uint8  = 1      // stable non-zero ANT+ transmission type
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
		"speed_period", antplus.SpeedChannelPeriod)

	state := broadcasterState{log: log, dev: device}
	if err := state.startSession(ctx); err != nil {
		return err
	}
	defer state.endSession(context.Background()) // ensure clean shutdown if context cancelled

	// Broadcast ticker. ANT+ channel period is 8182/32768 ≈ 0.24985s. The
	// chip handles its own timing on a real ANT+ slot — our ticker just
	// drives when we hand it the next page. A slightly fast ticker is fine;
	// the chip will buffer.
	tickInterval := time.Second * time.Duration(antplus.PowerChannelPeriod) / 32768
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	summaryTicker := time.NewTicker(30 * time.Second)
	defer summaryTicker.Stop()

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
		case <-ticker.C:
			if err := state.maybeBroadcast(ctx); err != nil {
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

	broadcasts        uint64
	nonZeroBroadcasts uint64
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

func (s *broadcasterState) startSession(ctx context.Context) error {
	if s.active {
		return nil
	}
	s.log.Info("ant+ opening power-meter and bike-speed channels")
	s.powerEncoder = antplus.PowerEncoder{}
	if err := s.openChannel(ctx, powerChannelNumber, powerDeviceNumber, antplus.PowerDeviceType, antplus.PowerChannelPeriod); err != nil {
		return err
	}
	if err := s.openChannel(ctx, speedChannelNumber, speedDeviceNumber, antplus.SpeedDeviceType, antplus.SpeedChannelPeriod); err != nil {
		return err
	}
	s.active = true
	return nil
}

func (s *broadcasterState) openChannel(ctx context.Context, channel uint8, deviceNumber uint16, deviceType uint8, period uint16) error {
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
	s.log.Info("ant channel opened", "channel", channel, "device_number", deviceNumber, "device_type", deviceType, "period", period)
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
}

func (s *broadcasterState) maybeBroadcast(ctx context.Context) error {
	if !s.active {
		return nil
	}
	powerPage := s.powerEncoder.EncodePage10(s.power, s.cadence)
	if err := s.dev.SendBroadcastData(ctx, powerChannelNumber, powerPage[:]); err != nil {
		return fmt.Errorf("ant broadcast power: %w", err)
	}

	speedPage := s.speedEncoder.EncodePage0()
	if err := s.dev.SendBroadcastData(ctx, speedChannelNumber, speedPage[:]); err != nil {
		return fmt.Errorf("ant broadcast speed: %w", err)
	}
	s.broadcasts++
	if s.power > 0 || s.cadence > 0 {
		s.nonZeroBroadcasts++
	}
	return nil
}

func (s *broadcasterState) logSummary() {
	s.log.Info("ant broadcast summary",
		"active", s.active,
		"power", s.power,
		"cadence", s.cadence,
		"broadcasts", s.broadcasts,
		"non_zero_broadcasts", s.nonZeroBroadcasts)
}
