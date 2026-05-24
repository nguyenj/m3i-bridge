package keiser

// DropoutFilter smooths over transient zero readings from a Keiser bike, where
// an occasional advert ships power=0 or cadence=0 despite the rider continuing
// to pedal. When only one of the two values is zero and the other is positive,
// the filter substitutes the previously-seen positive value.
//
// A true stop (both values reach zero) is NOT patched; once we've seen a zero
// in both, we accept it.
type DropoutFilter struct {
	prev    Advert
	hasPrev bool
}

// Apply returns curr with single-field transient zeros replaced by the
// last seen positive value. The filter's internal state always tracks the
// raw incoming sample (not the fixed one), so two consecutive samples at zero
// are not patched on the second.
func (f *DropoutFilter) Apply(curr Advert) Advert {
	fixed := curr
	if f.hasPrev {
		if curr.PowerWatts == 0 && curr.CadenceRPM > 0 && f.prev.PowerWatts > 0 {
			fixed.PowerWatts = f.prev.PowerWatts
		}
		if curr.CadenceRPM == 0 && curr.PowerWatts > 0 && f.prev.CadenceRPM > 0 {
			fixed.CadenceRPM = f.prev.CadenceRPM
		}
	}
	f.prev = curr
	f.hasPrev = true
	return fixed
}
