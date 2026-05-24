package keiser

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// build constructs a 19-byte Keiser advert with the given field values. Helper
// that keeps the test cases compact and lets us exercise specific bytes.
func build(opts func(b []byte)) []byte {
	buf := make([]byte, PayloadLen)
	buf[0], buf[1] = 0x02, 0x01
	if opts != nil {
		opts(buf)
	}
	return buf
}

func TestParse_RealtimeMain(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	buf := build(func(b []byte) {
		b[2] = 6                                    // version major
		b[3] = 32                                   // version minor (new firmware)
		b[4] = 0                                    // data type: realtime main
		b[5] = 42                                   // equipment id
		binary.LittleEndian.PutUint16(b[6:], 905)   // cadence 90.5 rpm -> /10 = 90
		binary.LittleEndian.PutUint16(b[8:], 1450)  // hr 145.0 bpm -> /10 = 145
		binary.LittleEndian.PutUint16(b[10:], 215)  // power 215 W
		binary.LittleEndian.PutUint16(b[12:], 1234) // calories
		b[14] = 12                                  // duration min
		b[15] = 34                                  // duration sec
		binary.LittleEndian.PutUint16(b[16:], 95)   // distance 9.5 km (msb=0)
		b[18] = 17                                  // gear
	})

	got, err := Parse(buf, now)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := Advert{
		Received:       now,
		VersionMajor:   6,
		VersionMinor:   32,
		DataType:       0,
		EquipmentID:    42,
		CadenceRPM:     90,
		HeartRateBPM:   145,
		PowerWatts:     215,
		Calories:       1234,
		DurationMin:    12,
		DurationSec:    34,
		DistanceTenths: 95,
		DistanceUnits:  9,
		DistanceMetric: true,
		Gear:           17,
	}
	if got != want {
		t.Errorf("Parse mismatch\n got: %+v\nwant: %+v", got, want)
	}

	if got.DataType.Classify() != ModeRealtimeMain {
		t.Errorf("Classify = %v, want ModeRealtimeMain", got.DataType.Classify())
	}
	if !got.DataType.Classify().IsRealtime() {
		t.Error("IsRealtime should be true for DT=0")
	}
	if !got.IsNewFirmware() {
		t.Error("IsNewFirmware should be true for 6.32")
	}
}

func TestParse_RealtimeInterval(t *testing.T) {
	buf := build(func(b []byte) {
		b[2], b[3] = 6, 30
		b[4] = 128 // realtime interval 1 (128 - 127)
		binary.LittleEndian.PutUint16(b[10:], 250)
	})
	got, err := Parse(buf, time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.DataType.Classify() != ModeRealtimeInterval {
		t.Errorf("Classify = %v, want ModeRealtimeInterval", got.DataType.Classify())
	}
	if !got.DataType.Classify().IsRealtime() {
		t.Error("IsRealtime should be true for DT=128")
	}
}

func TestParse_ReviewModeIsIgnoredByCaller(t *testing.T) {
	// Parser still returns the data; classification flags it as review.
	for _, dt := range []uint8{1, 16, 32} {
		buf := build(func(b []byte) { b[4] = dt })
		got, err := Parse(buf, time.Now())
		if err != nil {
			t.Fatalf("Parse(DT=%d): %v", dt, err)
		}
		if got.DataType.Classify() != ModeReview {
			t.Errorf("DT=%d Classify = %v, want ModeReview", dt, got.DataType.Classify())
		}
		if got.DataType.Classify().IsRealtime() {
			t.Errorf("DT=%d IsRealtime should be false", dt)
		}
	}
}

func TestParse_UnknownDataType(t *testing.T) {
	// Values not in 0, 1-32, or 128-227 are protocol-illegal but we should
	// classify them as unknown without panicking.
	for _, dt := range []uint8{33, 127, 228, 255} {
		buf := build(func(b []byte) { b[4] = dt })
		got, err := Parse(buf, time.Now())
		if err != nil {
			t.Fatalf("Parse(DT=%d): %v", dt, err)
		}
		if got.DataType.Classify() != ModeUnknown {
			t.Errorf("DT=%d Classify = %v, want ModeUnknown", dt, got.DataType.Classify())
		}
	}
}

func TestParse_OldFirmwareTimeoutBucket(t *testing.T) {
	buf := build(func(b []byte) {
		b[2], b[3] = 6, 29 // new threshold is 30, so 29 is "old"
	})
	got, err := Parse(buf, time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.IsNewFirmware() {
		t.Error("IsNewFirmware should be false for 6.29")
	}
}

func TestParse_DistanceImperial(t *testing.T) {
	buf := build(func(b []byte) {
		binary.LittleEndian.PutUint16(b[16:], 0x8000|123) // miles, value=12.3
	})
	got, err := Parse(buf, time.Now())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.DistanceMetric {
		t.Error("DistanceMetric should be false when MSB set")
	}
	if got.DistanceUnits != 12 {
		t.Errorf("DistanceUnits = %d, want 12", got.DistanceUnits)
	}
	if got.DistanceTenths != 123 {
		t.Errorf("DistanceTenths = %d, want 123", got.DistanceTenths)
	}
}

func TestParse_BadMagic(t *testing.T) {
	buf := make([]byte, PayloadLen)
	buf[0], buf[1] = 0xFF, 0xFF
	_, err := Parse(buf, time.Now())
	if !errors.Is(err, ErrBadMagic) {
		t.Errorf("err = %v, want ErrBadMagic", err)
	}
}

func TestParse_TooShort(t *testing.T) {
	_, err := Parse([]byte{0x02, 0x01, 0x06}, time.Now())
	if !errors.Is(err, ErrShortPayload) {
		t.Errorf("err = %v, want ErrShortPayload", err)
	}
}
