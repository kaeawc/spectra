package daemonprocess

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

func countingCollect(calls *atomic.Int32) CollectFunc {
	return func(context.Context) ([]process.Info, error) {
		n := calls.Add(1)
		return []process.Info{{PID: int(n), RSSKiB: 10}}, nil
	}
}

func newClockSvc(c CollectFunc) (*Service, *clock.Fake) {
	clk := clock.NewFake(time.Unix(1000, 0))
	return NewService(Options{Collect: c, Clock: clk}), clk
}

func TestListCachesWithinTTLAndExpires(t *testing.T) {
	var calls atomic.Int32
	s, clk := newClockSvc(countingCollect(&calls))
	ctx := context.Background()
	a, _ := s.List(ctx, false)
	clk.Advance(DefaultTTL - time.Millisecond)
	b, _ := s.List(ctx, false)
	if calls.Load() != 1 || a.SampledAt != b.SampledAt {
		t.Fatalf("calls=%d, want cache hit", calls.Load())
	}
	clk.Advance(time.Millisecond)
	c, err := s.List(ctx, false)
	if err != nil || calls.Load() != 2 || c.Processes[0].PID != 2 {
		t.Fatalf("calls=%d snap=%+v err=%v, want recollect", calls.Load(), c, err)
	}
}

func TestListRefreshBypassesCache(t *testing.T) {
	var calls atomic.Int32
	s, _ := newClockSvc(countingCollect(&calls))
	_, _ = s.List(context.Background(), false)
	_, _ = s.List(context.Background(), true)
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestListSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	s, _ := newClockSvc(func(context.Context) ([]process.Info, error) {
		calls.Add(1)
		<-release
		return []process.Info{{PID: 1}}, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, err := s.List(context.Background(), i%2 == 0)
			if err != nil || snap.Processes == nil {
				t.Errorf("snap=%+v err=%v", snap, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1", calls.Load())
	}
}

func TestListHungCollectorTimesOutAndReleases(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var hang atomic.Bool
	hang.Store(true)
	s := NewService(Options{Timeout: 30 * time.Millisecond, Clock: clock.NewFake(time.Unix(1, 0)), Collect: func(context.Context) ([]process.Info, error) {
		if hang.Load() {
			<-block
		}
		return []process.Info{{PID: 1}}, nil
	}})
	if _, err := s.List(context.Background(), false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline", err)
	}
	hang.Store(false)
	snap, err := s.List(context.Background(), false)
	if err != nil || len(snap.Processes) != 1 {
		t.Fatalf("not released/uncached: %+v %v", snap, err)
	}
}

func TestListPanicReturnsErrorToAllWaiters(t *testing.T) {
	gate := make(chan struct{})
	s, _ := newClockSvc(func(context.Context) ([]process.Info, error) {
		<-gate
		panic("boom")
	})
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { _, err := s.List(context.Background(), false); errs <- err }()
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	for i := 0; i < 3; i++ {
		if err := <-errs; err == nil {
			t.Fatal("want panic error")
		}
	}
}

func TestListCancelledCallerReturns(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s, _ := newClockSvc(func(context.Context) ([]process.Info, error) { <-block; return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := s.List(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

// The payload rows must be what `spectra process --json` prints: the same
// process.Info values, RSS-descending.
func TestPayloadMatchesCLIRows(t *testing.T) {
	in := func() []process.Info {
		return []process.Info{
			{PID: 1, PPID: 0, User: "u", Command: "a", FullCommandLine: "a -x", RSSKiB: 100, CPUPct: 1.5, CPUPercent: 1.5, StartedAt: time.Unix(5, 0).UTC()},
			{PID: 2, PPID: 1, Command: "b", FullCommandLine: "b", RSSKiB: 900, ThreadCount: 4, VSZBytes: 7, OpenFDs: 3},
		}
	}
	s, _ := newClockSvc(func(context.Context) ([]process.Info, error) { return in(), nil })
	snap, err := s.List(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	want := in()
	want[0], want[1] = want[1], want[0]
	got, _ := json.Marshal(snap.Processes)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("rows differ:\n%s\n%s", got, exp)
	}
	raw, _ := json.Marshal(snap)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if _, ok := m["sampled_at"]; !ok || len(m) != 2 {
		t.Fatalf("payload keys: %s", raw)
	}
}

func TestNoCollectionWithoutSubscribers(t *testing.T) {
	var calls atomic.Int32
	s := NewService(Options{Collect: countingCollect(&calls), Interval: 5 * time.Millisecond})
	time.Sleep(60 * time.Millisecond)
	if calls.Load() != 0 || s.Subscribers() != 0 {
		t.Fatalf("collected %d times with zero clients", calls.Load())
	}
}

func TestSubscribeStreamsThenStopsOnDone(t *testing.T) {
	var calls atomic.Int32
	s := NewService(Options{Collect: countingCollect(&calls), Interval: 10 * time.Millisecond, TTL: time.Millisecond})
	done := make(chan struct{})
	got := make(chan Snapshot, 16)
	s.Subscribe(done, func(method string, p any) error {
		if method != NotifyUpdate {
			t.Errorf("method %q", method)
		}
		got <- p.(Snapshot)
		return nil
	})
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("no update")
		}
	}
	close(done)
	deadline := time.Now().Add(2 * time.Second)
	for s.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Subscribers() != 0 {
		t.Fatal("subscriber not removed")
	}
	time.Sleep(30 * time.Millisecond) // let an in-flight collection finish
	n := calls.Load()
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != n {
		t.Fatalf("collection continued after last subscriber left: %d -> %d", n, calls.Load())
	}
}

func TestMethodsOverDaemonSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spdp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	svc := NewService(Options{Interval: 10 * time.Millisecond, TTL: time.Millisecond, Collect: func(context.Context) ([]process.Info, error) {
		return []process.Info{{PID: 1, RSSKiB: 1}, {PID: 2, RSSKiB: 2}}, nil
	}})
	RegisterMethods(server, svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	var client *daemonclient.Client
	for i := 0; i < 200 && client == nil; i++ {
		if c, err := daemonclient.Dial(ctx, paths.Socket); err == nil {
			client = c
		} else {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if client == nil {
		t.Fatal("daemon did not start")
	}
	st, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{MethodList, MethodSubscribe} {
		if !slices.Contains(st.Methods, m) {
			t.Fatalf("daemon.status methods %v missing %s", st.Methods, m)
		}
	}
	var snap Snapshot
	if err := client.Call(ctx, MethodList, map[string]bool{"refresh": true}, &snap); err != nil || len(snap.Processes) != 2 || snap.Processes[0].PID != 2 || snap.SampledAt.IsZero() {
		t.Fatalf("process.list: %+v %v", snap, err)
	}
	updates := make(chan Snapshot, 4)
	client.OnNotification(func(method string, raw json.RawMessage) {
		var s Snapshot
		if method == NotifyUpdate && json.Unmarshal(raw, &s) == nil {
			updates <- s
		}
	})
	var sub map[string]bool
	if err := client.Call(ctx, MethodSubscribe, nil, &sub); err != nil || !sub["subscribed"] {
		t.Fatalf("subscribe: %v %v", sub, err)
	}
	select {
	case u := <-updates:
		if len(u.Processes) != 2 {
			t.Fatalf("update %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no process.update")
	}
	client.Close()
	deadline := time.Now().Add(2 * time.Second)
	for svc.Subscribers() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if svc.Subscribers() != 0 {
		t.Fatal("subscriber not removed on disconnect")
	}
}

type blockingRunner struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, _ proc.Cmd) (proc.Result, error) {
	close(r.started)
	<-ctx.Done()
	close(r.cancelled)
	return proc.Result{}, ctx.Err()
}

// The default collector must hand ctx to its ps runner so the timeout kills it.
func TestDefaultCollectCancelsRunnerOnTimeout(t *testing.T) {
	r := &blockingRunner{started: make(chan struct{}), cancelled: make(chan struct{})}
	s := NewService(Options{Timeout: 50 * time.Millisecond, Collect: DefaultCollect(process.CollectOptions{OS: hostos.Darwin, Runner: r})})
	if _, err := s.List(context.Background(), false); err == nil {
		t.Fatal("want timeout error")
	}
	select {
	case <-r.started:
	default:
		t.Fatal("runner never ran")
	}
	select {
	case <-r.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("runner ctx was not cancelled on timeout")
	}
}

func TestEmptyCollectionIsErrorNotCached(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	s, clk := newClockSvc(func(context.Context) ([]process.Info, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, ErrNoProcesses
		}
		return []process.Info{{PID: 1}}, nil
	})
	if _, err := s.List(context.Background(), false); !errors.Is(err, ErrNoProcesses) {
		t.Fatalf("err=%v", err)
	}
	fail.Store(false)
	clk.Advance(time.Millisecond)
	snap, err := s.List(context.Background(), false)
	if err != nil || len(snap.Processes) != 1 || calls.Load() != 2 {
		t.Fatalf("failure was cached: %+v %v calls=%d", snap, err, calls.Load())
	}
	// DefaultCollect maps zero rows to the error.
	empty := DefaultCollect(process.CollectOptions{OS: hostos.Darwin, Runner: proc.NewFake()})
	if _, err := empty(context.Background()); err == nil {
		t.Fatal("zero rows must be an error")
	}
}

func TestSubscribeSkipsFailedTicks(t *testing.T) {
	var calls atomic.Int32
	s := NewService(Options{Interval: 10 * time.Millisecond, TTL: time.Millisecond, Collect: func(context.Context) ([]process.Info, error) {
		if calls.Add(1) <= 2 {
			return nil, ErrNoProcesses
		}
		return []process.Info{{PID: 9}}, nil
	}})
	done := make(chan struct{})
	defer close(done)
	got := make(chan Snapshot, 8)
	s.Subscribe(done, func(_ string, p any) error { got <- p.(Snapshot); return nil })
	select {
	case snap := <-got:
		if len(snap.Processes) != 1 || calls.Load() < 3 {
			t.Fatalf("pushed empty/failed update: %+v calls=%d", snap, calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update")
	}
}
