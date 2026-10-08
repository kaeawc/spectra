package snapshot

import (
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// ProcessStart returns the start time that, together with the PID,
// identifies one process lifetime for sample history. It is truncated to
// whole seconds because collectors differ in precision (ps lstart is
// second-resolution) and the identity must compare equal across runs.
// Returns the zero time when the start time is unknown.
func ProcessStart(p process.Info) time.Time {
	start := p.StartTime
	if start.IsZero() {
		start = p.StartedAt
	}
	if start.IsZero() {
		return time.Time{}
	}
	return start.UTC().Truncate(time.Second)
}

// ProcessStartsByPID maps each PID with a known start time to its
// ProcessStart, so JVM samples can borrow the identity of the matching
// process entry.
func ProcessStartsByPID(procs []process.Info) map[int]time.Time {
	out := make(map[int]time.Time, len(procs))
	for _, p := range procs {
		if start := ProcessStart(p); !start.IsZero() {
			out[p.PID] = start
		}
	}
	return out
}
