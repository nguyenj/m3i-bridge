package keiser

import "testing"

// mkAdvert builds an Advert with just power and cadence set — the only fields
// the dropout filter inspects.
func mkAdvert(power, cadence uint16) Advert {
	return Advert{PowerWatts: power, CadenceRPM: cadence}
}

func TestDropoutFilter_NoPriorSampleIsPassthrough(t *testing.T) {
	var f DropoutFilter
	got := f.Apply(mkAdvert(0, 0))
	if got.PowerWatts != 0 || got.CadenceRPM != 0 {
		t.Errorf("first sample should pass through unchanged, got %+v", got)
	}
}

func TestDropoutFilter_TransientZeroPowerIsPatched(t *testing.T) {
	var f DropoutFilter
	f.Apply(mkAdvert(200, 90)) // establish prev with positive power
	got := f.Apply(mkAdvert(0, 90))
	if got.PowerWatts != 200 {
		t.Errorf("PowerWatts = %d, want 200 (patched from prev)", got.PowerWatts)
	}
	if got.CadenceRPM != 90 {
		t.Errorf("CadenceRPM = %d, want 90", got.CadenceRPM)
	}
}

func TestDropoutFilter_TransientZeroCadenceIsPatched(t *testing.T) {
	var f DropoutFilter
	f.Apply(mkAdvert(200, 90))
	got := f.Apply(mkAdvert(200, 0))
	if got.CadenceRPM != 90 {
		t.Errorf("CadenceRPM = %d, want 90", got.CadenceRPM)
	}
}

func TestDropoutFilter_BothZeroIsNotPatched(t *testing.T) {
	var f DropoutFilter
	f.Apply(mkAdvert(200, 90))
	got := f.Apply(mkAdvert(0, 0))
	if got.PowerWatts != 0 || got.CadenceRPM != 0 {
		t.Errorf("simultaneous zeros should pass through (true stop), got %+v", got)
	}
}

func TestDropoutFilter_SecondConsecutiveZeroIsNotPatched(t *testing.T) {
	// The filter stores prev = curr (raw, not fixed), so once a zero shows up
	// twice the filter accepts it, meaning a genuine stop won't be hidden.
	var f DropoutFilter
	f.Apply(mkAdvert(200, 90))
	f.Apply(mkAdvert(0, 90))        // first zero: patched, prev <- {0,90}
	got := f.Apply(mkAdvert(0, 90)) // second zero in a row
	if got.PowerWatts != 0 {
		t.Errorf("PowerWatts = %d, want 0 (prev was zero too)", got.PowerWatts)
	}
}

func TestDropoutFilter_ZeroBeforeAnyPositiveStaysZero(t *testing.T) {
	var f DropoutFilter
	f.Apply(mkAdvert(0, 0)) // prev = {0,0}, but neither side ever was positive
	got := f.Apply(mkAdvert(0, 90))
	if got.PowerWatts != 0 {
		t.Errorf("PowerWatts = %d, want 0 (no positive prev to borrow)", got.PowerWatts)
	}
}
