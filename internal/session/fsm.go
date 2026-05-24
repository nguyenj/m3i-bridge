// Package session implements the Keiser bridge's session state machine.
//
// The machine consumes parsed Keiser adverts and produces high-level events
// for downstream consumers (the ANT+ broadcaster). It encodes the timing
// semantics needed for Keiser realtime BLE adverts:
//
//   - StatsTimeoutNew = 1s, StatsTimeoutOld = 7s (firmware-dependent stats
//     freshness window; zeros are emitted when exceeded but the session stays
//     alive)
//   - BikeTimeout = 60s (no realtime advert for this long ends the session)
//
// Review-mode adverts (DataType 1-32) are filtered out by the FSM entirely:
// they neither start a session nor refresh timers, so a bike replaying past
// intervals never accidentally drives ANT+ output.
package session

import (
	"time"

	"github.com/nguyenj/m3i-bridge/internal/keiser"
)

// Keiser session timing constants.
const (
	StatsTimeoutNew = 1 * time.Second  // KEISER_STATS_TIMEOUT_NEW
	StatsTimeoutOld = 7 * time.Second  // KEISER_STATS_TIMEOUT_OLD
	BikeTimeout     = 60 * time.Second // KEISER_BIKE_TIMEOUT
)

// State enumerates the FSM's possible states. NewSession and Ended are
// transient — the machine only ever rests in NoBike, Active, or Stale.
type State uint8

const (
	StateNoBike State = iota
	StateActive
	StateStale
)

// String makes State printable for slog and tests.
func (s State) String() string {
	switch s {
	case StateNoBike:
		return "NO_BIKE"
	case StateActive:
		return "ACTIVE"
	case StateStale:
		return "STALE"
	default:
		return "UNKNOWN"
	}
}

// EventType classifies a session event.
type EventType uint8

const (
	EventSessionStarted EventType = iota
	EventStatsUpdated
	EventSessionStale
	EventSessionEnded
)

// Event is one transition or stats update emitted by the FSM.
type Event struct {
	Type EventType
	At   time.Time

	// Populated for EventStatsUpdated and EventSessionStarted. Power and
	// Cadence are the dropout-filtered values caller-side; the FSM does no
	// further smoothing.
	Power   uint16
	Cadence uint16

	// Populated only when the event came from a realtime Keiser advert. Timeout
	// events that emit zero power/cadence intentionally leave DistanceValid
	// false so the ANT+ speed sensor does not treat the timeout as a bike
	// distance reset.
	DistanceValid  bool
	DistanceTenths uint16
	DistanceMetric bool
}

// Clock abstracts wall time so the FSM can be unit-tested with a fake.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// FSM is the session state machine. Use New to construct one, then drive it
// with Observe (one call per parsed advert) and Tick (one call when you want
// it to re-evaluate timeouts without new input — typically once per second
// on a ticker, or whenever an external loop needs an opinion).
//
// All methods return zero or more Events. The FSM is single-goroutine-only.
type FSM struct {
	clock        Clock
	statsTimeout time.Duration

	state    State
	lastSeen time.Time // last realtime advert received
	power    uint16
	cadence  uint16
}

// New constructs an FSM. statsTimeout is initially StatsTimeoutOld; the first
// realtime advert that classifies as new-firmware will switch it to
// StatsTimeoutNew automatically.
func New(clock Clock) *FSM {
	if clock == nil {
		clock = realClock{}
	}
	return &FSM{
		clock:        clock,
		statsTimeout: StatsTimeoutOld,
		state:        StateNoBike,
	}
}

// State returns the current state. Useful for logging and tests.
func (f *FSM) State() State { return f.state }

// Observe processes one parsed Keiser advert and returns any events that
// resulted. Review-mode and unknown-mode adverts return no events and do not
// affect state.
func (f *FSM) Observe(a keiser.Advert) []Event {
	if !a.DataType.Classify().IsRealtime() {
		return nil
	}

	now := a.Received
	if a.IsNewFirmware() {
		f.statsTimeout = StatsTimeoutNew
	}

	events := f.advance(now) // process any timeouts that expired before this advert

	switch f.state {
	case StateNoBike:
		f.state = StateActive
		f.power = a.PowerWatts
		f.cadence = a.CadenceRPM
		f.lastSeen = now
		events = append(events, Event{
			Type: EventSessionStarted, At: now,
			Power: a.PowerWatts, Cadence: a.CadenceRPM,
			DistanceValid:  true,
			DistanceTenths: a.DistanceTenths,
			DistanceMetric: a.DistanceMetric,
		})
	case StateActive, StateStale:
		prevState := f.state
		f.state = StateActive
		f.power = a.PowerWatts
		f.cadence = a.CadenceRPM
		f.lastSeen = now
		events = append(events, Event{
			Type: EventStatsUpdated, At: now,
			Power: a.PowerWatts, Cadence: a.CadenceRPM,
			DistanceValid:  true,
			DistanceTenths: a.DistanceTenths,
			DistanceMetric: a.DistanceMetric,
		})
		_ = prevState
	}
	return events
}

// Tick re-evaluates timeouts at the current clock time without taking any new
// input. Call this on a periodic ticker (e.g. 250ms) so the FSM can transition
// to Stale/Ended even when adverts have stopped arriving.
func (f *FSM) Tick() []Event {
	return f.advance(f.clock.Now())
}

// advance runs the timeout machine against the supplied "now". Returns any
// events that fired. Idempotent and safe to call repeatedly.
func (f *FSM) advance(now time.Time) []Event {
	var events []Event
	if f.state == StateNoBike {
		return events
	}

	since := now.Sub(f.lastSeen)

	switch f.state {
	case StateActive:
		if since >= BikeTimeout {
			f.state = StateNoBike
			events = append(events, Event{Type: EventSessionEnded, At: now})
		} else if since >= f.statsTimeout {
			f.state = StateStale
			f.power, f.cadence = 0, 0
			events = append(events,
				Event{Type: EventSessionStale, At: now},
				Event{Type: EventStatsUpdated, At: now, Power: 0, Cadence: 0},
			)
		}
	case StateStale:
		if since >= BikeTimeout {
			f.state = StateNoBike
			events = append(events, Event{Type: EventSessionEnded, At: now})
		}
	}
	return events
}
