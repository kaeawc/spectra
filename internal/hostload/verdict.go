// Package hostload answers "can this host take another job, and if not, who
// is using it?": an admit/hold/backoff verdict from load and available memory,
// plus attribution of CPU and RSS to processes, families and repo worktrees.
package hostload

import (
	"fmt"
	"math"
)

// Verdict is the admission decision.
type Verdict string

const (
	Admit   Verdict = "admit"
	Hold    Verdict = "hold"
	Backoff Verdict = "backoff"
)

// ExitCode maps a verdict to the `spectra load` process exit status.
func (v Verdict) ExitCode() int {
	switch v {
	case Hold:
		return 3
	case Backoff:
		return 4
	default:
		return 0
	}
}

const gib = float64(1 << 30)

// Reading is one point-in-time host measurement.
type Reading struct {
	Load1      float64
	CPUs       int
	AvailBytes uint64
	// MemKnown is false when available memory could not be read.
	MemKnown bool
}

// Thresholds are the admission limits.
type Thresholds struct {
	MaxLoadPerCPU float64
	MinFreeGB     float64 // GiB
	BackoffFreeGB float64 // GiB
}

// Decision is a verdict plus the single number that decided it.
type Decision struct {
	Verdict   Verdict
	Reason    string
	LoadLimit float64
}

// Decide applies backoff first, then hold, then admit. A reading exactly at
// a threshold passes it.
func Decide(r Reading, t Thresholds) Decision {
	cpus := r.CPUs
	if cpus < 1 {
		cpus = 1
	}
	// The OS reports load to 2 decimals; round so 1.1 x 3 is 3.30, not 3.3000000000000003.
	limit := math.Round(t.MaxLoadPerCPU*float64(cpus)*100) / 100
	d := Decision{LoadLimit: limit}
	availGB := float64(r.AvailBytes) / gib

	switch {
	case r.MemKnown && availGB < t.BackoffFreeGB:
		d.Verdict = Backoff
		d.Reason = fmt.Sprintf("available memory %.1f GiB is below the backoff threshold %.1f GiB", availGB, t.BackoffFreeGB)
	case r.Load1 >= limit:
		d.Verdict = Hold
		d.Reason = fmt.Sprintf("1-minute load %.2f is at or above the limit %.2f (%.2g x %d CPUs)", r.Load1, limit, t.MaxLoadPerCPU, cpus)
	case !r.MemKnown:
		// Fail closed: this gates starting more work.
		d.Verdict = Hold
		d.Reason = "available memory could not be read"
	case availGB < t.MinFreeGB:
		d.Verdict = Hold
		d.Reason = fmt.Sprintf("available memory %.1f GiB is below the minimum %.1f GiB", availGB, t.MinFreeGB)
	default:
		d.Verdict = Admit
		d.Reason = fmt.Sprintf("1-minute load %.2f is below the limit %.2f and available memory %.1f GiB is at least %.1f GiB", r.Load1, limit, availGB, t.MinFreeGB)
	}
	return d
}
