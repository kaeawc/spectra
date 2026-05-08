package snapshot

import "time"

// JVMSample is one historical point of JVM observability for a single PID.
// It is a deliberately compact subset of jvm.Info — enough to compute
// trend slopes without re-deserializing whole snapshots.
type JVMSample struct {
	PID       int       `json:"pid"`
	At        time.Time `json:"at"`
	OldGenPct float64   `json:"old_gen_pct"`
	FGC       int       `json:"fgc"`     // cumulative full-GC count
	FGCT      float64   `json:"fgct"`    // cumulative full-GC seconds
	HeapMB    int64     `json:"heap_mb"` // jstat OC + EC + survivor in MiB; 0 if unknown
}

// JVMHistory is a slice of JVMSamples in chronological order (oldest first).
// Empty / nil is the legitimate "no history available" state — callers must
// treat absence as "fall back to point-in-time rules", not as an error.
type JVMHistory []JVMSample

// SamplesFor returns just the samples for one PID, preserving order.
// Returns nil (not empty slice) when there are zero matches so callers can
// branch on `samples == nil` to mean "no history."
func (h JVMHistory) SamplesFor(pid int) []JVMSample {
	if len(h) == 0 {
		return nil
	}
	var out []JVMSample
	for _, s := range h {
		if s.PID == pid {
			out = append(out, s)
		}
	}
	return out
}
