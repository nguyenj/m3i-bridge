package antplus

import (
	"encoding/binary"
	"testing"
)

func TestEncodePage10_LayoutAndCounters(t *testing.T) {
	var enc PowerEncoder
	page := enc.EncodePage10(150, 80)

	if page[0] != PowerPageStandard {
		t.Errorf("page[0] = %#x, want %#x", page[0], PowerPageStandard)
	}
	if page[1] != 1 {
		t.Errorf("event_count = %d, want 1 (first call)", page[1])
	}
	if page[2] != 0xFF {
		t.Errorf("pedal_power = %#x, want 0xFF", page[2])
	}
	if page[3] != 80 {
		t.Errorf("cadence = %d, want 80", page[3])
	}
	if got := binary.LittleEndian.Uint16(page[4:6]); got != 150 {
		t.Errorf("accumulated_power = %d, want 150", got)
	}
	if got := binary.LittleEndian.Uint16(page[6:8]); got != 150 {
		t.Errorf("instantaneous_power = %d, want 150", got)
	}
}

func TestEncodeCalibrationResponse(t *testing.T) {
	page := EncodeCalibrationResponse()

	if page[0] != PowerPageCalibration {
		t.Fatalf("page[0] = %#x, want %#x", page[0], PowerPageCalibration)
	}
	if page[1] != CalibrationSuccess {
		t.Fatalf("calibration id = %#x, want success", page[1])
	}
	if page[2] != 0xFF || page[3] != 0xFF || page[4] != 0xFF || page[5] != 0xFF {
		t.Fatalf("reserved/autozero bytes = % x, want ff ff ff ff", page[2:6])
	}
	if got := binary.LittleEndian.Uint16(page[6:8]); got != 0 {
		t.Fatalf("calibration data = %d, want 0", got)
	}
}

func TestEncodePage10_AccumulatesAcrossCalls(t *testing.T) {
	var enc PowerEncoder
	enc.EncodePage10(100, 80)
	enc.EncodePage10(150, 80)
	page := enc.EncodePage10(200, 80)

	if page[1] != 3 {
		t.Errorf("event_count = %d, want 3 (third call)", page[1])
	}
	if got := binary.LittleEndian.Uint16(page[4:6]); got != 450 {
		t.Errorf("accumulated_power = %d, want 450", got)
	}
}

func TestEncodePage10_AccumulatorWrapsAt16Bit(t *testing.T) {
	enc := PowerEncoder{accumulatedPower: 0xFFF0}
	page := enc.EncodePage10(0x20, 0) // 0xFFF0 + 0x20 = 0x10010 -> wraps to 0x10
	if got := binary.LittleEndian.Uint16(page[4:6]); got != 0x10 {
		t.Errorf("accumulated_power = %#x, want 0x0010 (wrapped)", got)
	}
}

func TestEncodePage10_EventCountWrapsAt8Bit(t *testing.T) {
	enc := PowerEncoder{eventCount: 0xFF}
	page := enc.EncodePage10(100, 80)
	if page[1] != 0 {
		t.Errorf("event_count = %d, want 0 (wrapped)", page[1])
	}
}

func TestEncodePage10_CadenceInvalidSentinel(t *testing.T) {
	// Cadence 0xFF is the ANT+ "invalid" sentinel — we should clamp to 0xFF
	// only for values that exceed a real rpm reading (>=0xFF). For 0 cadence
	// we still report 0 (rider not pedaling), not 0xFF.
	var enc PowerEncoder

	page := enc.EncodePage10(150, 0)
	if page[3] != 0 {
		t.Errorf("cadence=0 byte=%d, want 0 (zero is legitimate)", page[3])
	}

	page = enc.EncodePage10(150, 300)
	if page[3] != 0xFF {
		t.Errorf("cadence=300 byte=%#x, want 0xFF (clamped)", page[3])
	}
}
