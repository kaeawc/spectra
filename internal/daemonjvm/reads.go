package daemonjvm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/jvm"
)

// Per-pid read methods. Each returns exactly the JSON the matching
// `spectra jvm <cmd> --json` prints, built by the same internal/jvm function
// the CLI calls, and fails with the CLI's stderr message. Nothing is cached.
const (
	// MethodExplain serves `spectra jvm explain --json`.
	MethodExplain = "jvm.explain"
	// MethodGCStats serves `spectra jvm gc-stats --json`.
	MethodGCStats = "jvm.gc_stats"
	// MethodHeapHistogram serves `spectra jvm heap-histogram --json`.
	MethodHeapHistogram = "jvm.heap_histogram"
	// MethodThreadDump serves `spectra jvm thread-dump --json`.
	MethodThreadDump = "jvm.thread_dump"
	// MethodVMMemory serves `spectra jvm vm-memory --json`.
	MethodVMMemory = "jvm.vm_memory"
	// MethodNMT serves `spectra jvm nmt [detail|diff] --json`.
	MethodNMT = "jvm.nmt"

	// DefaultReadTimeout bounds one per-pid read; jvm.explain adds its
	// trend window on top.
	DefaultReadTimeout = 60 * time.Second
	// MaxExplainSamples caps jvm.explain's samples.
	MaxExplainSamples = 120
	// MaxExplainTrendWindow caps (samples-1) * interval for jvm.explain.
	MaxExplainTrendWindow = 10 * time.Minute
	// defaultExplainInterval matches the CLI's --interval default.
	defaultExplainInterval = time.Second
)

// RunnerFunc returns the JDK tool runner for one call. It should kill its
// children when ctx ends.
type RunnerFunc func(ctx context.Context) jvm.CmdRunner

// PIDParams is the params object of the methods that take only a pid.
type PIDParams struct {
	PID int `json:"pid"`
}

// ExplainParams carry `spectra jvm explain`'s flags. Zero Samples means 1,
// zero IntervalMS means 1000, and a nil SoftRefs means true, as in the CLI.
type ExplainParams struct {
	PID        int   `json:"pid"`
	Samples    int   `json:"samples,omitempty"`
	IntervalMS int64 `json:"interval_ms,omitempty"`
	SoftRefs   *bool `json:"soft_refs,omitempty"`
}

// HeapHistogramParams carry `spectra jvm heap-histogram`'s flags. Zero
// Suspects ranks jvm.DefaultHistogramSuspects classes.
type HeapHistogramParams struct {
	PID      int `json:"pid"`
	Suspects int `json:"suspects,omitempty"`
}

// NMTParams select the `spectra jvm nmt` read: "summary" (default),
// "detail", or "diff".
type NMTParams struct {
	PID  int    `json:"pid"`
	Mode string `json:"mode,omitempty"`
}

// RegisterReadMethods registers the per-pid read methods on server.
func RegisterReadMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodExplain, svc.explainRPC)
	server.Register(MethodGCStats, svc.gcStatsRPC)
	server.Register(MethodHeapHistogram, svc.heapHistogramRPC)
	server.Register(MethodThreadDump, svc.threadDumpRPC)
	server.Register(MethodVMMemory, svc.vmMemoryRPC)
	server.Register(MethodNMT, svc.nmtRPC)
}

func validPID(pid int) error {
	if pid <= 0 {
		return daemon.InvalidParams("positive pid required")
	}
	return nil
}

// decodePID decodes params into dst and validates the pid it reports.
func decodePID(req *daemon.Request, dst any, pid *int) error {
	if err := decodeParams(req, dst); err != nil {
		return err
	}
	return validPID(*pid)
}

func (s *Service) gcStatsRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p PIDParams
	if err := decodePID(req, &p, &p.PID); err != nil {
		return nil, err
	}
	return s.call(ctx, req, MethodGCStats, s.readTimeout, func(ctx context.Context) (any, error) {
		return jvm.ReadGCStats(p.PID, s.runner(ctx))
	})
}

func (s *Service) threadDumpRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p PIDParams
	if err := decodePID(req, &p, &p.PID); err != nil {
		return nil, err
	}
	return s.call(ctx, req, MethodThreadDump, s.readTimeout, func(ctx context.Context) (any, error) {
		raw, err := jvm.CaptureThreadDump(p.PID, s.runner(ctx))
		if err != nil {
			return nil, err
		}
		return jvm.NewThreadDumpReport(raw, s.clock.Now()), nil
	})
}

func (s *Service) vmMemoryRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p PIDParams
	if err := decodePID(req, &p, &p.PID); err != nil {
		return nil, err
	}
	return s.call(ctx, req, MethodVMMemory, s.readTimeout, func(ctx context.Context) (any, error) {
		return jvm.CollectVMMemoryDiagnostics(p.PID, s.runner(ctx)), nil
	})
}

func (s *Service) heapHistogramRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p HeapHistogramParams
	if err := decodePID(req, &p, &p.PID); err != nil {
		return nil, err
	}
	if p.Suspects < 0 {
		return nil, daemon.InvalidParams("suspects must not be negative")
	}
	return s.call(ctx, req, MethodHeapHistogram, s.readTimeout, func(ctx context.Context) (any, error) {
		return jvm.ReadHeapHistogram(p.PID, p.Suspects, s.runner(ctx))
	})
}

func (s *Service) nmtRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p NMTParams
	if err := decodePID(req, &p, &p.PID); err != nil {
		return nil, err
	}
	switch p.Mode {
	case "", jvm.NMTModeSummary, jvm.NMTModeDetail, jvm.NMTModeDiff:
	default:
		return nil, daemon.InvalidParams("mode must be summary, detail, or diff")
	}
	return s.call(ctx, req, MethodNMT, s.readTimeout, func(ctx context.Context) (any, error) {
		return jvm.ReadNMT(p.PID, p.Mode, s.runner(ctx))
	})
}

// explainOptions validates p and fills the CLI's defaults.
func (p ExplainParams) explainOptions() (jvm.ExplainOptions, error) {
	if err := validPID(p.PID); err != nil {
		return jvm.ExplainOptions{}, err
	}
	if p.Samples < 0 || p.Samples > MaxExplainSamples {
		return jvm.ExplainOptions{}, daemon.InvalidParams("samples must be between 1 and %d", MaxExplainSamples)
	}
	if p.IntervalMS < 0 || p.IntervalMS > MaxExplainTrendWindow.Milliseconds() {
		return jvm.ExplainOptions{}, daemon.InvalidParams("interval_ms must be between 0 and %d", MaxExplainTrendWindow.Milliseconds())
	}
	opts := jvm.ExplainOptions{Samples: max(p.Samples, 1), Interval: time.Duration(p.IntervalMS) * time.Millisecond, SoftRefs: true}
	if opts.Interval == 0 {
		opts.Interval = defaultExplainInterval
	}
	if p.SoftRefs != nil {
		opts.SoftRefs = *p.SoftRefs
	}
	if time.Duration(opts.Samples-1)*opts.Interval > MaxExplainTrendWindow {
		return jvm.ExplainOptions{}, daemon.InvalidParams("(samples-1) * interval_ms must not exceed %s", MaxExplainTrendWindow)
	}
	return opts, nil
}

func (s *Service) explainRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p ExplainParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	opts, err := p.explainOptions()
	if err != nil {
		return nil, err
	}
	timeout := s.readTimeout + time.Duration(opts.Samples-1)*opts.Interval
	return s.call(ctx, req, MethodExplain, timeout, func(ctx context.Context) (any, error) {
		opts.CmdRunner = s.runner(ctx)
		return jvm.ReadExplanation(ctx, p.PID, opts)
	})
}

type readResult struct {
	value any
	err   error
}

// call runs fn under timeout, cancelling it when the client goes away. fn
// runs on its own goroutine so one that ignores ctx still releases the
// caller at the deadline, and a panic becomes an error. A value produced
// after ctx ended may be partial, so it is discarded.
func (s *Service) call(parent context.Context, req *daemon.Request, name string, timeout time.Duration, fn func(context.Context) (any, error)) (any, error) {
	rctx, rcancel := requestContext(parent, req.Done)
	defer rcancel()
	ctx, cancel := context.WithTimeout(rctx, timeout)
	defer cancel()
	ch := make(chan readResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- readResult{err: fmt.Errorf("%s: panicked: %v", name, r)}
			}
		}()
		v, err := fn(ctx)
		ch <- readResult{value: v, err: err}
	}()
	var r readResult
	select {
	case r = <-ch:
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && rctx.Err() == nil {
			return nil, fmt.Errorf("%s: timed out after %s: %w", name, timeout, err)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return r.value, r.err
}
