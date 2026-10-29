package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

const (
	// jvmHistoryStartDelay keeps the first pass off the daemon's startup path.
	jvmHistoryStartDelay = 10 * time.Second
	// jvmHistoryFailLimit is how many consecutive failed passes escalate the
	// log to Warn and stretch the cadence (jvmHistoryBackoff).
	jvmHistoryFailLimit = 3
	jvmHistoryBackoff   = 4
	// jvmHistoryStopGrace is added to the pass timeout when waiting for the
	// sampler to exit at shutdown.
	jvmHistoryStopGrace = 5 * time.Second
)

// ErrJPSUnavailable is returned by the default JVMSource when jps could not
// run, so an empty result does not mean there are no JVMs.
var ErrJPSUnavailable = errors.New("jps unavailable")

// JVMSource observes the running JVMs once: it returns a snapshot whose JVMs
// carry GC and (for JVMs started with NativeMemoryTracking) NMT readings, whose
// Processes carry the start times that identify each lifetime, and whose
// TakenAt stamps the samples. Implementations must honor ctx.
type JVMSource func(ctx context.Context) (snapshot.Snapshot, error)

// JVMHistoryStore persists a snapshot's JVMs as samples (store.DB does).
type JVMHistoryStore interface {
	AttachJVMHistory(context.Context, *snapshot.Snapshot) error
}

// NewJVMSource is the production JVMSource. It reuses the collection path of
// `spectra rules` (jvm.CollectAllStatus with SampleNMT, which only probes
// JVMs that opted into NMT) and routes every child process through runner so
// ctx cancellation kills jcmd/jstat/jps.
func NewJVMSource(runner proc.Runner, os hostos.Kind, clk clock.Clock) JVMSource {
	if clk == nil {
		clk = clock.System{}
	}
	return func(ctx context.Context) (snapshot.Snapshot, error) {
		opts := jvm.CollectOptions{
			CmdRunner: func(name string, args ...string) ([]byte, error) {
				return proc.Output(ctx, runner, name, args...)
			},
			CmdRunnerContext: func(c context.Context, name string, args ...string) ([]byte, error) {
				return proc.Output(c, runner, name, args...)
			},
			SampleNMT: true,
		}
		infos, jpsOK := jvm.CollectAllStatus(ctx, opts)
		snap := snapshot.Snapshot{TakenAt: clk.Now().UTC(), JVMs: infos}
		if !jpsOK {
			return snap, ErrJPSUnavailable
		}
		if len(infos) > 0 {
			snap.Processes = process.CollectAll(ctx, process.CollectOptions{Runner: runner, OS: os, Now: clk.Now})
		}
		return snap, nil
	}
}

type jvmSamplerState struct {
	source   JVMSource
	store    JVMHistoryStore
	failures int
	// jpsDown is set once the no-JDK condition has been warned about, so it
	// is announced once and not on every pass.
	jpsDown bool
}

// initJVMSampler wires the sampler when jvm_history.enabled is set and a
// history store is available; otherwise s.jvm stays nil and nothing runs.
func (s *Service) initJVMSampler() {
	cfg := s.opts.Config.JVMHistory
	if !cfg.Enabled {
		return
	}
	hist := s.opts.JVMHistory
	if hist == nil {
		hist, _ = s.opts.Store.(JVMHistoryStore)
	}
	if hist == nil {
		s.opts.Logger.Warn("jvm history sampler disabled: store cannot persist jvm samples")
		return
	}
	src := s.opts.JVMSource
	if src == nil {
		src = NewJVMSource(s.opts.Runner, s.opts.OS, s.opts.Clock)
	}
	s.jvm = &jvmSamplerState{source: src, store: hist}
}

// startJVMSampler runs the sampler loop when enabled and returns a stop
// function that cancels it and waits for the pass in flight, so the store is
// not written after Run returns (the daemon closes it next). The pass context
// is cancelled first and persistence honors it (lock waits abort and roll
// back), so the wait is short; it is
// capped at the pass budget plus a grace period so a write stuck in
// uninterruptible I/O, or a store that ignores ctx, cannot hang shutdown.
func (s *Service) startJVMSampler(ctx context.Context) (stop func()) {
	if s.jvm == nil {
		return func() {}
	}
	jctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.runJVMSampler(jctx) }()
	return func() {
		cancel()
		timer := time.NewTimer(s.opts.Config.JVMHistory.Timeout + jvmHistoryStopGrace)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			s.opts.Logger.Warn("jvm history sampler still running after shutdown wait; store writes may race close")
		}
	}
}

func (s *Service) runJVMSampler(ctx context.Context) {
	base := s.opts.Config.JVMHistory.Interval
	timer := time.NewTimer(min(jvmHistoryStartDelay, base))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_ = s.tickJVM(ctx)
		timer.Reset(s.jvmNextInterval())
	}
}

func (s *Service) jvmNextInterval() time.Duration {
	s.jvmMu.Lock()
	defer s.jvmMu.Unlock()
	base := s.opts.Config.JVMHistory.Interval
	if s.jvm.failures >= jvmHistoryFailLimit {
		return base * jvmHistoryBackoff
	}
	return base
}

// tickJVM runs one bounded sampling pass: collect, then persist through
// AttachJVMHistory. It is a no-op when the sampler is disabled or no JVM is
// running. Failures are rate-limited in the log and returned for callers
// that care; a cancelled parent ctx is not a failure.
func (s *Service) tickJVM(parent context.Context) error {
	if s.jvm == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, s.opts.Config.JVMHistory.Timeout)
	defer cancel()
	snap, err := s.collectJVMs(ctx)
	if err == nil && len(snap.JVMs) == 0 {
		s.jvmSucceeded()
		s.opts.Logger.Debug("jvm history: no running JVMs")
		return nil
	}
	if err == nil {
		if err = s.jvm.store.AttachJVMHistory(ctx, &snap); err != nil {
			err = fmt.Errorf("jvm history persist: %w", err)
		}
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("jvm history pass exceeded %s: %w", s.opts.Config.JVMHistory.Timeout, err)
		}
		s.jvmFailed(err)
		return err
	}
	s.jvmSucceeded()
	s.opts.Logger.Debug("jvm history sampled", "jvms", len(snap.JVMs))
	return nil
}

// collectJVMs enforces the time budget even for a source that ignores ctx:
// the pass is abandoned when ctx expires (the default source's children are
// killed by the same ctx).
func (s *Service) collectJVMs(ctx context.Context) (snapshot.Snapshot, error) {
	type result struct {
		snap snapshot.Snapshot
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		snap, err := s.jvm.source(ctx)
		ch <- result{snap, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.snap, fmt.Errorf("jvm history collect: %w", r.err)
		}
		return r.snap, nil
	case <-ctx.Done():
		return snapshot.Snapshot{}, fmt.Errorf("jvm history collect: %w", ctx.Err())
	}
}

func (s *Service) jvmFailed(err error) {
	s.jvmMu.Lock()
	s.jvm.failures++
	n := s.jvm.failures
	firstJPS := errors.Is(err, ErrJPSUnavailable) && !s.jvm.jpsDown
	if firstJPS {
		s.jvm.jpsDown = true
	}
	s.jvmMu.Unlock()
	switch {
	case errors.Is(err, ErrJPSUnavailable):
		// A host without a JDK fails every pass; announce it once.
		if firstJPS {
			s.opts.Logger.Warn("jvm history sampling unavailable: jps not found; backing off until it appears", "error", err)
		} else {
			s.opts.Logger.Debug("jvm history sampling failed", "error", err)
		}
	case n == jvmHistoryFailLimit:
		s.opts.Logger.Warn("jvm history sampling failing; backing off", "error", err, "consecutive_failures", n)
	case n > jvmHistoryFailLimit && n%jvmHistoryFailLimit == 0:
		s.opts.Logger.Warn("jvm history sampling still failing", "error", err, "consecutive_failures", n)
	default:
		s.opts.Logger.Debug("jvm history sampling failed", "error", err)
	}
}

func (s *Service) jvmSucceeded() {
	s.jvmMu.Lock()
	n := s.jvm.failures
	s.jvm.failures = 0
	wasJPSDown := s.jvm.jpsDown
	s.jvm.jpsDown = false
	s.jvmMu.Unlock()
	if n >= jvmHistoryFailLimit || wasJPSDown {
		s.opts.Logger.Info("jvm history sampling recovered", "consecutive_failures", n)
	}
}
