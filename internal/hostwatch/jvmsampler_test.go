package hostwatch

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

var jvmT0 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

const jvmNMTArgs = "-Xmx2g -XX:NativeMemoryTracking=summary"

// jvmFixture drives the sampler with a fake clock and a scripted source over
// a real store, so what the rules see is what the daemon would have written.
type jvmFixture struct {
	svc   *Service
	db    *store.DB
	log   *logger.Capture
	clk   *clock.Fake
	calls atomic.Int32
	// internalKiB is the NMT "Internal" committed size reported on the next pass.
	internalKiB int64
	err         error
	jvms        int
	hist        snapshot.JVMHistory
}

type persistRecorder struct {
	f  *jvmFixture
	db *store.DB
}

func (p persistRecorder) AttachJVMHistory(ctx context.Context, snap *snapshot.Snapshot) error {
	err := p.db.AttachJVMHistory(ctx, snap)
	p.f.hist = snap.JVMHistory
	return err
}

func newJVMFixture(t *testing.T, mutate func(*Config)) *jvmFixture {
	t.Helper()
	f := &jvmFixture{db: testDB(t), log: logger.NewCapture(slog.LevelDebug), clk: clock.NewFake(jvmT0), jvms: 1, internalKiB: 100 * 1024}
	cfg := DefaultConfig()
	cfg.JVMHistory.Enabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	f.svc = NewService(ServiceOptions{
		Store: f.db, Config: cfg, ConfigSet: true, Logger: f.log, Clock: f.clk,
		Collector:  &sequenceCollector{},
		JVMHistory: persistRecorder{f: f, db: f.db},
		JVMSource: func(context.Context) (snapshot.Snapshot, error) {
			f.calls.Add(1)
			if f.err != nil {
				return snapshot.Snapshot{}, f.err
			}
			snap := snapshot.Snapshot{TakenAt: f.clk.Now()}
			for i := 0; i < f.jvms; i++ {
				snap.JVMs = append(snap.JVMs, jvm.Info{
					PID: 77 + i, MainClass: "leaky.App", VMArgs: jvmNMTArgs,
					GC:              &jvm.GCStats{OC: 1000, OU: 400 + float64(f.calls.Load())*50},
					NMTCommittedKiB: map[string]int64{"Internal": f.internalKiB},
				})
				snap.Processes = append(snap.Processes, process.Info{PID: 77 + i, StartTime: jvmT0.Add(-24 * time.Hour)})
			}
			return snap, nil
		},
	})
	return f
}

func (f *jvmFixture) tick(t *testing.T) {
	t.Helper()
	if err := f.svc.TickJVM(context.Background()); err != nil {
		t.Fatalf("TickJVM: %v", err)
	}
}

func (f *jvmFixture) count(level slog.Level) int { return len(f.log.FilterLevel(level)) }

func TestJVMSamplerDisabledByDefault(t *testing.T) {
	called := false
	svc := NewService(ServiceOptions{
		Store: testDB(t), Collector: &sequenceCollector{},
		JVMSource: func(context.Context) (snapshot.Snapshot, error) { called = true; return snapshot.Snapshot{}, nil },
	})
	if svc.jvm != nil {
		t.Fatal("sampler must be off with the default config")
	}
	if err := svc.TickJVM(context.Background()); err != nil || called {
		t.Fatalf("disabled TickJVM err=%v called=%v", err, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := svc.startJVMSampler(ctx)
	stop()
	cancel()
}

func TestJVMSamplerWritesSamplePerTick(t *testing.T) {
	f := newJVMFixture(t, nil)
	f.jvms = 2
	for i := 0; i < 3; i++ {
		f.tick(t)
		f.clk.Advance(5 * time.Minute)
	}
	for _, pid := range []int{77, 78} {
		got, err := f.db.GetRecentJVMSamples(context.Background(), pid, jvmT0.Add(-24*time.Hour), 0)
		if err != nil || len(got) != 3 {
			t.Fatalf("pid %d samples = %d, %v; want 3", pid, len(got), err)
		}
	}
	if f.calls.Load() != 3 {
		t.Fatalf("source calls = %d, want 3", f.calls.Load())
	}
}

func TestJVMSamplerSkipsWhenNoJVMs(t *testing.T) {
	f := newJVMFixture(t, nil)
	f.jvms = 0
	f.tick(t)
	f.hist = nil
	if f.calls.Load() != 1 || f.hist != nil || f.count(slog.LevelWarn) != 0 {
		t.Fatalf("no-JVM pass should be a quiet no-op: calls=%d hist=%v", f.calls.Load(), f.hist)
	}
}

func TestJVMSamplerBudgetAbandonsStuckSource(t *testing.T) {
	f := newJVMFixture(t, func(c *Config) { c.JVMHistory.Timeout = 50 * time.Millisecond })
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	f.svc.jvm.source = func(context.Context) (snapshot.Snapshot, error) { <-block; return snapshot.Snapshot{}, nil }
	start := time.Now()
	err := f.svc.TickJVM(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("pass ignored its time budget")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("budget error should name the budget: %v", err)
	}
}

func TestJVMSamplerParentCancelIsNotAFailure(t *testing.T) {
	f := newJVMFixture(t, nil)
	f.err = errors.New("boom")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 2*jvmHistoryFailLimit; i++ {
		if err := f.svc.TickJVM(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	}
	if f.svc.jvm.failures != 0 || f.count(slog.LevelWarn) != 0 {
		t.Fatalf("shutdown counted as failure: %d", f.svc.jvm.failures)
	}
}

func TestJVMSamplerFailureLoggingIsRateLimited(t *testing.T) {
	f := newJVMFixture(t, nil)
	f.err = ErrJPSUnavailable
	base := f.svc.opts.Config.JVMHistory.Interval
	for i := 1; i <= 3*jvmHistoryFailLimit; i++ {
		if err := f.svc.TickJVM(context.Background()); !errors.Is(err, ErrJPSUnavailable) {
			t.Fatalf("pass %d err = %v", i, err)
		}
		want := base
		if i >= jvmHistoryFailLimit {
			want = base * jvmHistoryBackoff
		}
		if got := f.svc.jvmNextInterval(); got != want {
			t.Fatalf("pass %d next interval = %s, want %s", i, got, want)
		}
	}
	// Warn only on the 3rd, 6th and 9th consecutive failure; the rest are debug.
	if warns := f.count(slog.LevelWarn); warns != 3 {
		t.Fatalf("warns = %d, want 3", warns)
	}
	if f.count(slog.LevelDebug) < 6 {
		t.Fatalf("intermediate failures should log at debug, got %d", f.count(slog.LevelDebug))
	}
	f.err = nil
	f.tick(t)
	if !f.log.HasMessage("recovered") || f.svc.jvmNextInterval() != base {
		t.Fatal("recovery must log and restore the cadence")
	}
}

func TestJVMSamplerPersistFailureCounts(t *testing.T) {
	f := newJVMFixture(t, nil)
	boom := errors.New("disk full")
	f.svc.jvm.store = failingHistory{boom}
	if err := f.svc.TickJVM(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped %v", err, boom)
	}
	if f.svc.jvm.failures != 1 {
		t.Fatalf("failures = %d", f.svc.jvm.failures)
	}
}

type failingHistory struct{ err error }

func (h failingHistory) AttachJVMHistory(context.Context, *snapshot.Snapshot) error { return h.err }

func TestJVMSamplerLoopStopsOnCancel(t *testing.T) {
	f := newJVMFixture(t, func(c *Config) {
		c.JVMHistory.Interval = 5 * time.Millisecond
		c.JVMHistory.Timeout = 5 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := f.svc.startJVMSampler(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for f.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()
	n := f.calls.Load()
	if n < 3 {
		t.Fatalf("loop never sampled repeatedly: %d", n)
	}
	time.Sleep(30 * time.Millisecond)
	if f.calls.Load() != n {
		t.Fatal("sampler kept running after stop")
	}
	if f.log.HasMessage("still running after shutdown") {
		t.Fatal("sampler did not exit promptly")
	}
}

func TestJVMSamplerRunStartsAndStopsWithService(t *testing.T) {
	f := newJVMFixture(t, func(c *Config) {
		c.JVMHistory.Interval = 5 * time.Millisecond
		c.JVMHistory.Timeout = 5 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.svc.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for f.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if f.calls.Load() < 2 {
		t.Fatal("Run never started the sampler")
	}
}

// TestJVMSamplerBuildsHistoryForTrendRules is the end-to-end claim of #519:
// N daemon passes alone give the stored history the rules need, with no
// `spectra rules` run involved.
func TestJVMSamplerBuildsHistoryForTrendRules(t *testing.T) {
	f := newJVMFixture(t, nil)
	for i := 0; i < 5; i++ {
		f.internalKiB = int64(100+20*i) * 1024 // +20 MiB per 5 minutes
		f.tick(t)
		if i < 4 {
			f.clk.Advance(5 * time.Minute)
		}
	}
	history := f.hist
	if n := len(history.SamplesFor(77)); n < rules.MinTrendSamples {
		t.Fatalf("heap-trend history has %d samples, want >= %d", n, rules.MinTrendSamples)
	}
	snap := snapshot.Snapshot{
		TakenAt:    f.clk.Now(),
		JVMs:       []jvm.Info{{PID: 77, MainClass: "leaky.App", VMArgs: jvmNMTArgs}},
		JVMHistory: history,
	}
	var found bool
	for _, fd := range rules.Evaluate(snap, rules.V1Catalog()) {
		if fd.RuleID == "jvm-native-memory-growth" && strings.Contains(fd.Subject, "Internal") {
			found = true
		}
	}
	if !found {
		t.Fatalf("jvm-native-memory-growth did not fire on %d daemon-built samples", len(history))
	}
}

// TestJVMSamplerSurvivesPrune checks the sampler and the hourly prune agree:
// fresh samples are retained, only samples past the retention window go.
func TestJVMSamplerSurvivesPrune(t *testing.T) {
	f := newJVMFixture(t, nil)
	now := time.Now().UTC()
	f.clk.Set(now.Add(-10 * 24 * time.Hour))
	f.tick(t) // older than the 7-day retention
	f.clk.Set(now)
	f.tick(t)
	if _, err := f.db.PruneJVMSamples(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	got, err := f.db.GetRecentJVMSamples(context.Background(), 77, jvmT0.Add(-24*time.Hour), 0)
	if err != nil || len(got) != 1 || got[0].At.Before(now.Add(-time.Minute)) {
		t.Fatalf("after prune samples = %v, %v; want only the fresh one", got, err)
	}
}

func jvmRunner(nmtCalls *[]string, jpsErr error) proc.Runner {
	return runnerFunc(func(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
		out := func(s string) (proc.Result, error) { return proc.Result{Stdout: []byte(s)}, nil }
		switch {
		case cmd.Name == "jps":
			if jpsErr != nil {
				return proc.Result{}, jpsErr
			}
			return out("41 plain.App\n42 nmt.App\n")
		case cmd.Name == "jcmd" && len(cmd.Args) >= 2 && cmd.Args[1] == "VM.command_line":
			if cmd.Args[0] == "42" {
				return out("VM Arguments:\njvm_args: " + jvmNMTArgs + "\n")
			}
			return out("VM Arguments:\njvm_args: -Xmx1g\n")
		case cmd.Name == "jcmd" && len(cmd.Args) >= 2 && cmd.Args[1] == "VM.native_memory":
			*nmtCalls = append(*nmtCalls, cmd.Args[0])
			return out("Total: reserved=1KB, committed=1KB\n-                     Internal (reserved=1KB, committed=2048KB)\n")
		}
		return proc.Result{ExitCode: 1}, errors.New("unavailable")
	})
}

func TestNewJVMSourceSamplesNMTOnlyForOptedInJVMs(t *testing.T) {
	var nmt []string
	src := NewJVMSource(jvmRunner(&nmt, nil), hostos.Linux, clock.NewFake(jvmT0))
	snap, err := src(context.Background())
	if err != nil || len(snap.JVMs) != 2 || !snap.TakenAt.Equal(jvmT0) {
		t.Fatalf("snap = %+v, %v", snap, err)
	}
	if len(nmt) != 1 || nmt[0] != "42" {
		t.Fatalf("NMT probed for %v, want only the NMT-enabled pid 42", nmt)
	}
}

func TestNewJVMSourceReportsMissingJPS(t *testing.T) {
	var nmt []string
	src := NewJVMSource(jvmRunner(&nmt, errors.New("not found")), hostos.Linux, nil)
	if _, err := src(context.Background()); !errors.Is(err, ErrJPSUnavailable) {
		t.Fatalf("err = %v, want ErrJPSUnavailable", err)
	}
}
