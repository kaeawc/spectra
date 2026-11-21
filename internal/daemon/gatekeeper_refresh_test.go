package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// spctlFake answers `codesign -dv` with a per-app CDHash and counts spctl
// runs per app plus peak concurrency.
type spctlFake struct {
	mu      sync.Mutex
	runs    map[string]int
	active  atomic.Int32
	peak    atomic.Int32
	onSpctl func(ctx context.Context)
}

func (f *spctlFake) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	app := cmd.Args[len(cmd.Args)-1]
	if cmd.Name == "codesign" {
		return proc.Result{Stderr: []byte("CDHash=hash-" + app + "\n")}, nil
	}
	n := f.active.Add(1)
	defer f.active.Add(-1)
	if n > f.peak.Load() {
		f.peak.Store(n)
	}
	f.mu.Lock()
	f.runs[app]++
	f.mu.Unlock()
	if f.onSpctl != nil {
		f.onSpctl(ctx)
	}
	if ctx.Err() != nil {
		return proc.Result{ExitCode: -1}, ctx.Err()
	}
	return proc.Result{Stderr: []byte(app + ": accepted\nsource=Notarized Developer ID\n")}, nil
}

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(k []byte) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[string(k)]
	return b, ok
}

func (s *memStore) Put(k, v []byte, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[string(k)] = v
	return nil
}

func newRefresher(f *spctlFake, apps []string, wait func(context.Context, time.Duration) bool, now func() time.Time) *GatekeeperRefresher {
	return NewGatekeeperRefresher(GatekeeperRefreshOptions{
		Apps: func() []string { return apps },
		Detect: detect.Options{
			OS:         hostos.Darwin,
			Runner:     f,
			SealCache:  &memStore{m: map[string][]byte{}},
			Gatekeeper: detect.NewGatekeeperAssessor(time.Second, 24*time.Hour, now),
		},
		Wait: wait,
	})
}

func TestGatekeeperRefresherAssessesStaleAppsOnceSerially(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	f := &spctlFake{runs: map[string]int{}}
	apps := []string{"/Applications/A.app", "/Applications/B.app", "/Applications/C.app"}
	r := newRefresher(f, apps, func(context.Context, time.Duration) bool { return true }, clk)

	if got := r.Pass(context.Background()); got != 3 {
		t.Fatalf("first pass assessed %d, want 3", got)
	}
	if got := r.Pass(context.Background()); got != 0 {
		t.Fatalf("second pass assessed %d fresh apps, want 0", got)
	}
	now = now.Add(25 * time.Hour)
	if got := r.Pass(context.Background()); got != 3 {
		t.Fatalf("pass after TTL assessed %d, want 3", got)
	}
	for _, app := range apps {
		if f.runs[app] != 2 {
			t.Fatalf("%s assessed %d times, want 2 (once per stale pass)", app, f.runs[app])
		}
	}
	if f.peak.Load() != 1 {
		t.Fatalf("peak spctl concurrency %d, want 1", f.peak.Load())
	}
}

func TestGatekeeperRefresherStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &spctlFake{runs: map[string]int{}}
	// Cancel during the first spctl run, as a daemon shutdown would.
	f.onSpctl = func(context.Context) { cancel() }
	apps := []string{"/Applications/A.app", "/Applications/B.app"}
	r := newRefresher(f, apps, func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }, nil)

	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop after cancel")
	}
	if f.runs[apps[0]] != 1 || f.runs[apps[1]] != 0 {
		t.Fatalf("runs after cancel = %v, want only the first app once", f.runs)
	}
	// The canceled assessment left nothing cached: a new pass re-runs it.
	if got := r.Pass(context.Background()); got != 2 {
		t.Fatalf("pass after cancel assessed %d, want 2", got)
	}
}
