package snapshot

import (
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// FDSample is one historical point of file-descriptor usage for a single PID.
// It is a deliberately compact subset of process.Info — enough to compute a
// leak slope without re-deserializing whole snapshots.
type FDSample struct {
	PID     int       `json:"pid"`
	At      time.Time `json:"at"`
	OpenFDs int       `json:"open_fds"`
}

// FDSampleFrom builds a sample from a live process.Info reading. Returns
// ok=false when the process reports no open descriptors (shallow mode, where
// OpenFDs is 0, or a collection that never ran).
func FDSampleFrom(p process.Info, at time.Time) (FDSample, bool) {
	if p.OpenFDs <= 0 {
		return FDSample{}, false
	}
	return FDSample{
		PID:     p.PID,
		At:      at.UTC(),
		OpenFDs: p.OpenFDs,
	}, true
}

// FDHistory is a slice of FDSamples in chronological order (oldest first).
// Empty / nil is the legitimate "no history available" state — callers must
// treat absence as "fall back to point-in-time rules", not as an error.
type FDHistory []FDSample

// SamplesFor returns just the samples for one PID, preserving order.
// Returns nil (not empty slice) when there are zero matches so callers can
// branch on `samples == nil` to mean "no history."
func (h FDHistory) SamplesFor(pid int) []FDSample {
	if len(h) == 0 {
		return nil
	}
	var out []FDSample
	for _, s := range h {
		if s.PID == pid {
			out = append(out, s)
		}
	}
	return out
}
