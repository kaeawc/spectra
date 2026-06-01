package serve

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestChurnTickCountsSpawnAndExit(t *testing.T) {
	tracker := newChurnTracker(nil)
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	app := "/Applications/Foo.app"

	tracker.tick(base, []process.Info{
		procInfo(10, 1, app, base.Add(-time.Minute)),
		procInfo(11, 10, app, base.Add(-time.Minute)),
	})
	got := tracker.tick(base.Add(time.Second), []process.Info{
		procInfo(10, 1, app, base.Add(-time.Minute)),
		procInfo(12, 10, app, base.Add(time.Second)),
	})

	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1", len(got))
	}
	if got[0].Spawns1s != 1 || got[0].Exits1s != 1 {
		t.Fatalf("sample = %+v, want Spawns1s=1 Exits1s=1", got[0])
	}
	if got[0].ChildrenCount != 2 {
		t.Fatalf("ChildrenCount = %d, want 2", got[0].ChildrenCount)
	}
}

func TestChurnRollingWindowBoundary(t *testing.T) {
	tracker := newChurnTracker(nil)
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	app := "/Applications/Foo.app"
	var last []AppChurnSample
	for i := 0; i < 70; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		last = tracker.tick(at, []process.Info{procInfo(100+i, 1, app, at)})
	}
	if len(last) != 1 {
		t.Fatalf("samples = %d, want 1", len(last))
	}
	if last[0].Spawns1m != 61 {
		t.Fatalf("Spawns1m = %d, want 61 across inclusive 60s window", last[0].Spawns1m)
	}
	if last[0].Exits1m != 61 {
		t.Fatalf("Exits1m = %d, want 61", last[0].Exits1m)
	}
}

func TestChurnPIDReuseAfterExitCountsExitThenSpawn(t *testing.T) {
	tracker := newChurnTracker(nil)
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	app := "/Applications/Foo.app"

	tracker.tick(base, []process.Info{procInfo(42, 1, app, base.Add(-time.Minute))})
	exit := tracker.tick(base.Add(time.Second), nil)
	spawn := tracker.tick(base.Add(2*time.Second), []process.Info{procInfo(42, 1, app, base.Add(2*time.Second))})

	if len(exit) != 1 || exit[0].Exits1s != 1 {
		t.Fatalf("exit sample = %+v, want one exit", exit)
	}
	if len(spawn) != 1 || spawn[0].Spawns1s != 1 {
		t.Fatalf("spawn sample = %+v, want one spawn", spawn)
	}
}

func TestSpawnFailureWatcherRestartsRunner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int32
	watcher := &spawnFailureWatcher{}
	watcher.run = func(context.Context, func(...spawnFailureEvent)) error {
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			return errors.New("stream died")
		}
		cancel()
		return nil
	}

	done := make(chan struct{})
	go func() {
		watcher.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not restart within 2s")
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatalf("run calls = %d, want at least 2", calls)
	}
}

func TestParseSpawnFailureLogLineExtractsAppPath(t *testing.T) {
	raw := []byte(`{"timestamp":"2026-05-04T12:00:00Z","processImagePath":"/Applications/Foo.app/Contents/MacOS/Foo","eventMessage":"posix_spawn failed"}`)
	event, ok := parseSpawnFailureLogLine(raw, time.Date(2026, 5, 4, 12, 1, 0, 0, time.UTC))
	if !ok {
		t.Fatal("parseSpawnFailureLogLine returned !ok")
	}
	if event.AppPath != "/Applications/Foo.app" {
		t.Fatalf("AppPath = %q, want /Applications/Foo.app", event.AppPath)
	}
	if !event.At.Equal(time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("At = %s", event.At)
	}
}

func BenchmarkChurnTick(b *testing.B) {
	tracker := newChurnTracker(nil)
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	procs := make([]process.Info, 200)
	for i := range procs {
		procs[i] = procInfo(1000+i, 1, "/Applications/Foo.app", base.Add(-time.Minute))
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tracker.tick(base.Add(time.Duration(i)*time.Second), procs)
	}
}

func procInfo(pid, ppid int, app string, start time.Time) process.Info {
	return process.Info{PID: pid, PPID: ppid, AppPath: app, StartTime: start}
}
