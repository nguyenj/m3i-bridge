package session

import (
	"testing"
	"time"

	"github.com/nguyenj/m3i-bridge/internal/keiser"
)

// fakeClock advances only when the test moves it forward.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Set(t time.Time)         { c.t = t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func realtimeAdvert(at time.Time, power, cadence uint16, newFirmware bool) keiser.Advert {
	major, minor := uint8(6), uint8(10)
	if newFirmware {
		minor = 32
	}
	return keiser.Advert{
		Received:       at,
		VersionMajor:   major,
		VersionMinor:   minor,
		DataType:       0, // realtime main
		PowerWatts:     power,
		CadenceRPM:     cadence,
		DistanceTenths: 95,
		DistanceMetric: true,
	}
}

func reviewAdvert(at time.Time) keiser.Advert {
	return keiser.Advert{
		Received:     at,
		VersionMajor: 6,
		VersionMinor: 32,
		DataType:     16, // review interval 16
	}
}

// findEvent returns the first event of the given type, or false if none.
func findEvent(events []Event, t EventType) (Event, bool) {
	for _, e := range events {
		if e.Type == t {
			return e, true
		}
	}
	return Event{}, false
}

func TestFSM_FirstRealtimeAdvertStartsSession(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)

	if fsm.State() != StateNoBike {
		t.Fatalf("initial state = %v, want NO_BIKE", fsm.State())
	}
	events := fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))
	if _, ok := findEvent(events, EventSessionStarted); !ok {
		t.Errorf("expected SessionStarted in %+v", events)
	}
	if fsm.State() != StateActive {
		t.Errorf("state = %v, want ACTIVE", fsm.State())
	}
}

func TestFSM_ReviewModeIsIgnored(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)

	events := fsm.Observe(reviewAdvert(clk.t))
	if len(events) != 0 {
		t.Errorf("review-mode advert produced events: %+v", events)
	}
	if fsm.State() != StateNoBike {
		t.Errorf("state changed to %v, want NO_BIKE", fsm.State())
	}
}

func TestFSM_OngoingAdvertsEmitStatsUpdated(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true)) // starts session

	clk.Advance(500 * time.Millisecond)
	events := fsm.Observe(realtimeAdvert(clk.t, 160, 82, true))
	if e, ok := findEvent(events, EventStatsUpdated); !ok || e.Power != 160 || e.Cadence != 82 {
		t.Errorf("got %+v, want StatsUpdated power=160 cadence=82", events)
	}
}

func TestFSM_RealtimeEventsCarryKeiserDistance(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)

	events := fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))
	e, ok := findEvent(events, EventSessionStarted)
	if !ok {
		t.Fatalf("expected SessionStarted in %+v", events)
	}
	if !e.DistanceValid || e.DistanceTenths != 95 || !e.DistanceMetric {
		t.Errorf("distance fields = valid:%v tenths:%d metric:%v, want valid 95 metric", e.DistanceValid, e.DistanceTenths, e.DistanceMetric)
	}
}

func TestFSM_StatsTimeoutNewFirmwareGoesStale(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	clk.Advance(StatsTimeoutNew + 100*time.Millisecond)
	events := fsm.Tick()
	if _, ok := findEvent(events, EventSessionStale); !ok {
		t.Errorf("expected SessionStale in %+v", events)
	}
	if fsm.State() != StateStale {
		t.Errorf("state = %v, want STALE", fsm.State())
	}
	if e, ok := findEvent(events, EventStatsUpdated); !ok || e.Power != 0 || e.Cadence != 0 {
		t.Errorf("expected StatsUpdated(0,0) in stale, got %+v", events)
	}
	if e, ok := findEvent(events, EventStatsUpdated); ok && e.DistanceValid {
		t.Errorf("stale StatsUpdated should not carry distance, got %+v", e)
	}
}

func TestFSM_StatsTimeoutOldFirmwareWaitsForFreshnessWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, false)) // old firmware

	clk.Advance(2 * time.Second)
	if events := fsm.Tick(); len(events) != 0 {
		t.Errorf("2s into old-firmware session should not be stale yet, got %+v", events)
	}
	clk.Advance(StatsTimeoutOld)
	if _, ok := findEvent(fsm.Tick(), EventSessionStale); !ok {
		t.Error("expected SessionStale after stats freshness window")
	}
}

func TestFSM_StaleSessionRecoversOnNewAdvert(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	clk.Advance(StatsTimeoutNew + time.Second)
	fsm.Tick() // -> stale

	if fsm.State() != StateStale {
		t.Fatalf("setup: expected STALE, got %v", fsm.State())
	}

	clk.Advance(500 * time.Millisecond)
	events := fsm.Observe(realtimeAdvert(clk.t, 175, 85, true))
	if e, ok := findEvent(events, EventStatsUpdated); !ok || e.Power != 175 {
		t.Errorf("expected StatsUpdated(175,85) on recovery, got %+v", events)
	}
	if fsm.State() != StateActive {
		t.Errorf("state = %v, want ACTIVE after recovery", fsm.State())
	}
}

func TestFSM_BikeTimeoutEndsSession(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	clk.Advance(BikeTimeout + 100*time.Millisecond)
	events := fsm.Tick()
	if _, ok := findEvent(events, EventSessionEnded); !ok {
		t.Errorf("expected SessionEnded in %+v", events)
	}
	if fsm.State() != StateNoBike {
		t.Errorf("state = %v, want NO_BIKE", fsm.State())
	}
}

func TestFSM_NewSessionAfterEnded(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	// End the first session
	clk.Advance(BikeTimeout + time.Second)
	fsm.Tick()
	if fsm.State() != StateNoBike {
		t.Fatalf("setup: expected NO_BIKE, got %v", fsm.State())
	}

	// New ride starts
	clk.Advance(time.Hour)
	events := fsm.Observe(realtimeAdvert(clk.t, 200, 90, true))
	if _, ok := findEvent(events, EventSessionStarted); !ok {
		t.Errorf("expected SessionStarted on new ride, got %+v", events)
	}
}

func TestFSM_ReviewAdvertDuringActiveSessionDoesNotResetTimers(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	// Inject a review advert; should be ignored.
	clk.Advance(500 * time.Millisecond)
	if events := fsm.Observe(reviewAdvert(clk.t)); len(events) != 0 {
		t.Errorf("review during active session emitted events: %+v", events)
	}

	// Real-time silence: stats timeout should still fire based on the original advert,
	// not refreshed by the review.
	clk.Advance(StatsTimeoutNew)
	if _, ok := findEvent(fsm.Tick(), EventSessionStale); !ok {
		t.Error("expected SessionStale to fire based on realtime advert age")
	}
}

func TestFSM_SparseRealtimeKeepsLastStatsWithinFreshnessWindow(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	fsm.Observe(realtimeAdvert(clk.t, 150, 80, true))

	clk.Advance(StatsTimeoutNew / 2)
	if events := fsm.Tick(); len(events) != 0 {
		t.Errorf("sparse realtime gap inside freshness window should not zero stats, got %+v", events)
	}
	if fsm.State() != StateActive {
		t.Errorf("state = %v, want ACTIVE", fsm.State())
	}
}

func TestFSM_NoEventsBeforeAnyAdvert(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	fsm := New(clk)
	clk.Advance(2 * time.Hour)
	if events := fsm.Tick(); len(events) != 0 {
		t.Errorf("ticks in NO_BIKE should be no-ops, got %+v", events)
	}
}
