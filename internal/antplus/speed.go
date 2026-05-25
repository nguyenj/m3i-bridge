package antplus

import (
	"encoding/binary"
	"time"
)

// Bike Speed channel constants per ANT+ device profile 0x7B.
const (
	SpeedDeviceType    uint8  = 0x7B
	SpeedChannelPeriod uint16 = 8118 // ~= 4.04 Hz for speed-only sensors.

	// The bridge exposes Keiser distance through a virtual wheel. Set the
	// paired Garmin speed sensor wheel size to this value so Garmin's recorded
	// distance matches the Keiser distance.
	VirtualWheelCircumferenceMM uint64 = 2000
)

// SpeedEncoder produces ANT+ Bicycle Speed payloads. It does not derive speed
// from power or cadence; it only converts Keiser's cumulative distance into
// the cumulative wheel-revolution counter required by the ANT+ speed profile.
type SpeedEncoder struct {
	haveDistance  bool
	lastDistance  uint64
	totalDistance uint64
	eventTime     uint16
}

// ObserveDistance updates the encoder with a Keiser cumulative distance value
// in tenths of km/miles. If the bike distance resets between sessions, the ANT+
// revolution counter stays monotonic and subsequent positive Keiser distance
// deltas are added to the existing counter.
func (e *SpeedEncoder) ObserveDistance(at time.Time, distanceTenths uint16, metric bool) {
	current := distanceTenthsToMillimeters(distanceTenths, metric)
	if !e.haveDistance {
		e.haveDistance = true
		e.lastDistance = current
		e.totalDistance = current
		e.eventTime = eventTime1024(at)
		return
	}

	if current <= e.lastDistance {
		e.lastDistance = current
		return
	}

	before := e.CumulativeRevolutions()
	e.totalDistance += current - e.lastDistance
	e.lastDistance = current
	if e.CumulativeRevolutions() != before {
		e.eventTime = eventTime1024(at)
	}
}

// EncodePage0 builds an 8-byte ANT+ Bicycle Speed payload:
//
//	[0] 0x00                     main data page
//	[1-3] 0xFF                   reserved
//	[4-5] speed_event_time       1/1024s, wraps at 64s
//	[6-7] cumulative_revolutions virtual wheel revolutions, wraps at 0xFFFF
func (e *SpeedEncoder) EncodePage0() [8]byte {
	var p [8]byte
	p[0] = 0x00
	p[1], p[2], p[3] = 0xFF, 0xFF, 0xFF
	binary.LittleEndian.PutUint16(p[4:6], e.eventTime)
	binary.LittleEndian.PutUint16(p[6:8], e.CumulativeRevolutions())
	return p
}

func (e *SpeedEncoder) CumulativeRevolutions() uint16 {
	return uint16(e.totalDistance / VirtualWheelCircumferenceMM)
}

func (e *SpeedEncoder) EventTime() uint16 { return e.eventTime }

func distanceTenthsToMillimeters(distanceTenths uint16, metric bool) uint64 {
	if metric {
		return uint64(distanceTenths) * 100_000 // 0.1 km
	}
	return uint64(distanceTenths) * 1_609_344 / 10 // 0.1 mile
}

func eventTime1024(at time.Time) uint16 {
	return uint16(at.UnixNano() * 1024 / int64(time.Second))
}
