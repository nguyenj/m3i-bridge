package antplus

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestSpeedEncoder_InitialPageIsStationary(t *testing.T) {
	var enc SpeedEncoder
	page := enc.EncodePage0()

	if page[0] != 0x00 {
		t.Errorf("page[0] = %#x, want 0x00", page[0])
	}
	if page[1] != 0xFF || page[2] != 0xFF || page[3] != 0xFF {
		t.Errorf("reserved bytes = [%#x %#x %#x], want all 0xFF", page[1], page[2], page[3])
	}
	if got := binary.LittleEndian.Uint16(page[4:6]); got != 0 {
		t.Errorf("event time = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(page[6:8]); got != 0 {
		t.Errorf("revolutions = %d, want 0", got)
	}
}

func TestSpeedEncoder_ConvertsMetricDistanceToVirtualWheelRevolutions(t *testing.T) {
	var enc SpeedEncoder
	at := time.Unix(10, 0)

	enc.ObserveDistance(at, 95, true) // 9.5 km
	page := enc.EncodePage0()

	if got, want := binary.LittleEndian.Uint16(page[6:8]), uint16(4750); got != want {
		t.Errorf("revolutions = %d, want %d", got, want)
	}
	if got, want := binary.LittleEndian.Uint16(page[4:6]), uint16(10240); got != want {
		t.Errorf("event time = %d, want %d", got, want)
	}
}

func TestSpeedEncoder_AdvancesOnlyFromKeiserDistanceDelta(t *testing.T) {
	var enc SpeedEncoder
	enc.ObserveDistance(time.Unix(10, 0), 95, true) // 9.5 km -> 4750 revs
	enc.ObserveDistance(time.Unix(12, 0), 96, true) // +0.1 km -> +50 revs

	if got, want := enc.CumulativeRevolutions(), uint16(4800); got != want {
		t.Errorf("revolutions = %d, want %d", got, want)
	}
	if got, want := enc.EventTime(), uint16(12288); got != want {
		t.Errorf("event time = %d, want %d", got, want)
	}
}

func TestSpeedEncoder_DistanceResetDoesNotDecrementANTCounter(t *testing.T) {
	var enc SpeedEncoder
	enc.ObserveDistance(time.Unix(10, 0), 96, true)
	enc.ObserveDistance(time.Unix(20, 0), 0, true)
	enc.ObserveDistance(time.Unix(25, 0), 1, true)

	if got, want := enc.CumulativeRevolutions(), uint16(4850); got != want {
		t.Errorf("revolutions = %d, want %d", got, want)
	}
}

func TestSpeedEncoder_ImperialDistance(t *testing.T) {
	var enc SpeedEncoder
	enc.ObserveDistance(time.Unix(10, 0), 10, false) // 1.0 mile

	if got, want := enc.CumulativeRevolutions(), uint16(804); got != want {
		t.Errorf("revolutions = %d, want %d", got, want)
	}
}
