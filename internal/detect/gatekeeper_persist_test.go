package detect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// diskVerdictStore opens the on-disk store a fresh process would, sharing
// dir and the injected clock.
func diskVerdictStore(dir string, now func() time.Time) *cache.TTLStore {
	s := cache.NewTTLStore(cache.NewShardedStore(dir, cache.KindSeal), nil)
	s.SetClock(now)
	return s
}

func TestVerdictPersistedAndReusedByNewProcess(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	const app, hash = "/Applications/App.app", "hash"

	first := &countingSpctl{}
	a1 := NewGatekeeperAssessor(time.Second, 24*time.Hour, clk)
	if v, ran := a1.assessPersist(context.Background(), first, diskVerdictStore(dir, clk), app, hash); !ran || v.status != "accepted" {
		t.Fatalf("first run = %+v ran=%v, want accepted after running spctl", v, ran)
	}

	// A new process: new assessor, new store instance over the same files.
	now = now.Add(23 * time.Hour)
	second := &countingSpctl{}
	a2 := NewGatekeeperAssessor(time.Second, 24*time.Hour, clk)
	v, ran := a2.assessPersist(context.Background(), second, diskVerdictStore(dir, clk), app, hash)
	if ran || second.calls.Load() != 0 || v.status != "accepted" || v.source != "Notarized Developer ID" {
		t.Fatalf("within TTL = %+v ran=%v calls=%d, want persisted accepted without spctl", v, ran, second.calls.Load())
	}

	// Another CDHash for the same path is a different binary.
	if _, ran := a2.assessPersist(context.Background(), second, diskVerdictStore(dir, clk), app, "other"); !ran {
		t.Fatal("a new CDHash reused the persisted verdict")
	}
}

func TestPersistedVerdictExpires(t *testing.T) {
	store := newMemSealCache() // ignores the store TTL so the assessor's own check is exercised
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	clk := func() time.Time { return now }
	const app, hash = "/Applications/App.app", "hash"
	NewGatekeeperAssessor(time.Second, 24*time.Hour, clk).assessPersist(context.Background(), &countingSpctl{}, store, app, hash)

	now = now.Add(25 * time.Hour)
	f := &countingSpctl{}
	if _, ran := NewGatekeeperAssessor(time.Second, 24*time.Hour, clk).assessPersist(context.Background(), f, store, app, hash); !ran || f.calls.Load() != 1 {
		t.Fatalf("expired verdict was reused (ran=%v calls=%d)", ran, f.calls.Load())
	}
	if store.puts != 2 {
		t.Fatalf("re-assessed verdict not re-persisted, puts=%d", store.puts)
	}
}

func TestTimeoutAndCancelNotPersisted(t *testing.T) {
	store := newMemSealCache()
	a := NewGatekeeperAssessor(10*time.Millisecond, time.Hour, nil)
	if v, _ := a.assessPersist(context.Background(), &blockingSpctl{}, store, "/Applications/Slow.app", "h"); v.err != GatekeeperErrorTimeout {
		t.Fatalf("assess = %+v, want timeout", v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if v, _ := a.assessPersist(ctx, &blockingSpctl{}, store, "/Applications/Slow.app", "h"); v.err != GatekeeperErrorCanceled {
		t.Fatalf("assess = %+v, want canceled", v)
	}
	if store.puts != 0 {
		t.Fatalf("inconclusive assessment persisted (%d puts)", store.puts)
	}
}

func TestDetectWithPersistsVerdictInSealCache(t *testing.T) {
	app := makeBundle(t, "Chrome")
	store := newMemSealCache()
	f := signingFake{toolFake: newToolFake(), codesign: codesignDevIDStapled, spctl: &countingSpctl{}}
	if _, err := detectDarwinRunner(t, app, f, Options{AssessGatekeeper: true, Gatekeeper: newTestAssessor(nil), SealCache: store}); err != nil {
		t.Fatal(err)
	}
	second := &countingSpctl{}
	f.spctl = second
	r, err := detectDarwinRunner(t, app, f, Options{AssessGatekeeper: true, Gatekeeper: newTestAssessor(nil), SealCache: store})
	if err != nil {
		t.Fatal(err)
	}
	if r.GatekeeperStatus != "accepted" || second.calls.Load() != 0 {
		t.Fatalf("second run status %q after %d spctl calls, want persisted accepted", r.GatekeeperStatus, second.calls.Load())
	}
}

func TestRefreshGatekeeper(t *testing.T) {
	store := newMemSealCache()
	spctl := &countingSpctl{}
	opts := Options{OS: hostos.Darwin, Gatekeeper: newTestAssessor(nil), SealCache: store,
		Runner: signingFake{toolFake: newToolFake(), codesign: codesignDevIDStapled, spctl: spctl}}
	if got := RefreshGatekeeper("/Applications/Chrome.app", opts); !got.Assessed || got.Status != "accepted" {
		t.Fatalf("first refresh = %+v", got)
	}
	if got := RefreshGatekeeper("/Applications/Chrome.app", opts); got.Assessed || got.Status != "accepted" || spctl.calls.Load() != 1 {
		t.Fatalf("fresh entry re-assessed: %+v calls=%d", got, spctl.calls.Load())
	}
	opts.Runner = signingFake{toolFake: newToolFake(), codesign: codesignUnsigned, spctl: spctl}
	if got := RefreshGatekeeper("/Applications/Unsigned.app", opts); got.Assessed || spctl.calls.Load() != 1 {
		t.Fatalf("bundle without CDHash assessed: %+v", got)
	}
	if got := RefreshGatekeeper("/x.app", Options{OS: hostos.Linux}); got.Assessed || got.Err == "" {
		t.Fatalf("linux refresh = %+v, want unsupported", got)
	}
}

// blockingCodesign blocks codesign until its context ends and records why.
type blockingCodesign struct {
	started chan struct{}
	err     chan error
}

func (f blockingCodesign) Run(ctx context.Context, cmd proc.Cmd) (proc.Result, error) {
	if cmd.Name != "codesign" {
		return proc.Result{}, errors.New("unexpected " + cmd.Name)
	}
	close(f.started)
	<-ctx.Done()
	f.err <- ctx.Err()
	return proc.Result{}, ctx.Err()
}

func TestRefreshGatekeeperCancelKillsCodesign(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := blockingCodesign{started: make(chan struct{}), err: make(chan error, 1)}
	done := make(chan GatekeeperRefresh, 1)
	go func() {
		done <- RefreshGatekeeper("/Applications/App.app", Options{OS: hostos.Darwin, Context: ctx, Runner: f, Gatekeeper: newTestAssessor(nil)})
	}()
	<-f.started
	cancel()
	select {
	case err := <-f.err:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("codesign context ended with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceling the refresh did not cancel codesign's context")
	}
	if got := <-done; got.Assessed {
		t.Fatalf("canceled refresh assessed: %+v", got)
	}
}
