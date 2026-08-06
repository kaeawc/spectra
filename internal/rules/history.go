package rules

import "github.com/kaeawc/spectra/internal/snapshot"

// History-aware predicates that operate on snapshot.JVMHistory.
//
// Design note: these are deliberately simple. A trend is "rising" when there
// are enough samples and the delta between the first and last sample exceeds
// a threshold. v1 ignores noise/jitter — the cost of a missed rising trend
// is one cycle of delayed alerting; the cost of a false rising signal is a
// regression of the very behavior we're trying to fix.

// MinTrendSamples is the minimum number of samples needed before any trend
// predicate can fire. Below this, "no opinion" (returns false) is correct.
const MinTrendSamples = 3

// MinOldGenRiseDelta is the percentage-points rise required between the
// oldest and newest sample in a window for an old-gen trend to count as
// "rising". 5pp over a few minutes is meaningful in a healthy heap.
const MinOldGenRiseDelta = 5.0

// RisingOldGenFor reports whether old-gen occupancy is rising for pid in the
// given history. Returns false when there is no history, fewer than
// MinTrendSamples observations, or the delta is below MinOldGenRiseDelta.
//
// "Rising" here is intentionally a coarse first/last comparison; it does not
// require monotonic increase. Most allocation pressure shows as a clear net
// gain over a short window, and tighter shape detection can come later.
func RisingOldGenFor(h snapshot.JVMHistory, pid int) bool {
	samples := h.SamplesFor(pid)
	if len(samples) < MinTrendSamples {
		return false
	}
	first := samples[0].OldGenPct
	last := samples[len(samples)-1].OldGenPct
	return last-first >= MinOldGenRiseDelta
}

// HasTrendFor reports whether there are enough samples for trend predicates
// to be meaningful. Rules use this to decide whether to apply a trend gate
// or fall back to point-in-time checks.
func HasTrendFor(h snapshot.JVMHistory, pid int) bool {
	return len(h.SamplesFor(pid)) >= MinTrendSamples
}

// HasFDTrendFor reports whether there are enough fd samples for trend
// predicates to be meaningful. Rules use this to decide whether to apply a
// trend gate or fall back to point-in-time checks. Mirrors HasTrendFor.
func HasFDTrendFor(h snapshot.FDHistory, pid int) bool {
	return len(h.SamplesFor(pid)) >= MinTrendSamples
}

// RisingFDsFor reports whether open-fd usage is climbing for pid in the given
// history. Returns false when there is no history or fewer than
// MinTrendSamples observations.
//
// Unlike the coarse first/last old-gen comparison, a descriptor leak shows as
// a monotone climb: allocation without release. "Rising" therefore requires
// the samples to be non-decreasing across the window AND the last sample to
// exceed the first (a net climb, not a flat high-water mark). A single dip —
// the process closing descriptors — is enough to disqualify the window, which
// is the conservative choice: the cost of a missed leak is one delayed cycle,
// the cost of a false leak signal is noise on healthy steady-state processes.
func RisingFDsFor(h snapshot.FDHistory, pid int) bool {
	samples := h.SamplesFor(pid)
	if len(samples) < MinTrendSamples {
		return false
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].OpenFDs < samples[i-1].OpenFDs {
			return false
		}
	}
	return samples[len(samples)-1].OpenFDs > samples[0].OpenFDs
}
