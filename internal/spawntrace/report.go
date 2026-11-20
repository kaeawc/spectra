package spawntrace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/process"
)

// Deps are the host seams a trace needs; tests replace them.
type Deps struct {
	Collect  func(ctx context.Context) []process.Info
	Ticker   func(time.Duration) (<-chan time.Time, func())
	Deadline func(time.Duration) (<-chan time.Time, func())
	Clock    clock.Clock
	// SelfPID's direct children (the tracer's own ps runs) are hidden.
	SelfPID int
}

// DefaultDeps polls the real process table.
func DefaultDeps() Deps {
	return Deps{
		Collect: func(ctx context.Context) []process.Info {
			// Skip per-process libproc detail and the physical-memory
			// lookup: spawn tracing only needs pid, ppid, command and start.
			return process.CollectAll(ctx, process.CollectOptions{
				DetailCollector: func([]process.Info) map[int]process.Details { return nil },
				MemoryBytes:     func() uint64 { return 0 },
			})
		},
		Ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		Deadline: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		},
		Clock:   clock.Default,
		SelfPID: os.Getpid(),
	}
}

// Options are a trace's inputs (the CLI's flags).
type Options struct {
	Interval time.Duration
	Duration time.Duration
	// PID scopes the trace to this process and its descendants; 0 is all.
	PID int
	// Progress, when set, runs after each collection (see Config.Progress).
	Progress func(elapsed time.Duration, events int)
}

// DefaultOptions are the CLI flag defaults.
func DefaultOptions() Options {
	return Options{Interval: 250 * time.Millisecond, Duration: 10 * time.Second}
}

var (
	// ErrInvalidOptions reports a non-positive interval or duration, or a
	// negative pid.
	ErrInvalidOptions = errors.New("interval and duration must be positive and pid must not be negative")
	// ErrIntervalTooLong reports an interval longer than the duration.
	ErrIntervalTooLong = errors.New("interval must not exceed duration")
	// ErrRootNotFound reports a scoped pid absent from the process table.
	ErrRootNotFound = errors.New("pid not found in the process table")
)

// Validate rejects the option values the CLI rejects as usage errors.
func (o Options) Validate() error {
	if o.Interval <= 0 || o.Duration <= 0 || o.PID < 0 {
		return ErrInvalidOptions
	}
	if o.Interval > o.Duration {
		return ErrIntervalTooLong
	}
	return nil
}

// Config builds the Run configuration for o.
func (o Options) Config(deps Deps, emit func(Event), notify func(string)) Config {
	return Config{
		Collect: deps.Collect, Ticker: deps.Ticker, Deadline: deps.Deadline, Clock: deps.Clock,
		Interval: o.Interval, Duration: o.Duration, Root: o.PID,
		Emit: emit, Notify: notify, IgnoreChildrenOf: deps.SelfPID, Progress: o.Progress,
	}
}

// Report is a whole bounded trace in one document: the events the CLI
// streams as JSON lines, then its final summary line.
type Report struct {
	Events []Event `json:"events"`
	// EventsDropped counts events past the cap; the summary still counts them.
	EventsDropped int `json:"events_dropped,omitempty"`
	// Notes are the one-off notices the CLI writes to stderr.
	Notes   []string `json:"notes,omitempty"`
	Summary Summary  `json:"summary"`
}

// Trace runs a trace to completion and collects it, keeping at most
// maxEvents events (0 keeps all). opts must already be valid.
func Trace(ctx context.Context, opts Options, deps Deps, maxEvents int) (Report, error) {
	rep := Report{Events: []Event{}}
	emit := func(e Event) {
		if maxEvents > 0 && len(rep.Events) >= maxEvents {
			rep.EventsDropped++
			return
		}
		rep.Events = append(rep.Events, e)
	}
	notify := func(msg string) { rep.Notes = append(rep.Notes, msg) }
	sum := Run(ctx, opts.Config(deps, emit, notify))
	if sum.RootNotFound {
		return Report{}, fmt.Errorf("pid %d: %w", opts.PID, ErrRootNotFound)
	}
	rep.Summary = sum
	return rep, nil
}
