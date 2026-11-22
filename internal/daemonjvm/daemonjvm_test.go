package daemonjvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func countingList(calls *atomic.Int32) ListFunc {
	return func(context.Context) ([]jvm.Info, bool) {
		n := calls.Add(1)
		return []jvm.Info{{PID: int(n)}}, true
	}
}

func TestListCachesWithinTTL(t *testing.T) {
	var calls atomic.Int32
	clk := clock.NewFake(time.Unix(1000, 0))
	s := NewService(Options{List: countingList(&calls), Clock: clk, TTL: 5 * time.Second})
	for i := 0; i < 3; i++ {
		infos, err := s.List(context.Background(), false)
		if err != nil || len(infos) != 1 || infos[0].PID != 1 {
			t.Fatalf("call %d: %v %v", i, infos, err)
		}
		clk.Advance(time.Second)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls = %d, want 1", got)
	}
}

func TestListRecollectsAfterTTL(t *testing.T) {
	var calls atomic.Int32
	clk := clock.NewFake(time.Unix(1000, 0))
	s := NewService(Options{List: countingList(&calls), Clock: clk, TTL: 5 * time.Second})
	if _, err := s.List(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	clk.Advance(5 * time.Second)
	infos, err := s.List(context.Background(), false)
	if err != nil || infos[0].PID != 2 || calls.Load() != 2 {
		t.Fatalf("infos=%v err=%v calls=%d", infos, err, calls.Load())
	}
}

func TestListRefreshBypassesCache(t *testing.T) {
	var calls atomic.Int32
	s := NewService(Options{List: countingList(&calls), Clock: clock.NewFake(time.Unix(1000, 0))})
	_, _ = s.List(context.Background(), false)
	infos, err := s.List(context.Background(), true)
	if err != nil || infos[0].PID != 2 || calls.Load() != 2 {
		t.Fatalf("infos=%v err=%v calls=%d", infos, err, calls.Load())
	}
	// The refreshed result now serves cached reads.
	if infos, _ := s.List(context.Background(), false); infos[0].PID != 2 || calls.Load() != 2 {
		t.Fatalf("after refresh: infos=%v calls=%d", infos, calls.Load())
	}
}

func TestListSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	list := func(context.Context) ([]jvm.Info, bool) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return []jvm.Info{{PID: 7}}, true
	}
	s := NewService(Options{List: list, Clock: clock.NewFake(time.Unix(1000, 0))})
	const n = 8
	var wg sync.WaitGroup
	results := make(chan int, n)
	// A canceled caller must not abort the shared collection.
	cancelCtx, cancel := context.WithCancel(context.Background())
	canceledErr := make(chan error, 1)
	go func() {
		_, err := s.List(cancelCtx, false)
		canceledErr <- err
	}()
	<-started
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			infos, err := s.List(context.Background(), false)
			if err != nil {
				t.Error(err)
				return
			}
			results <- infos[0].PID
		}()
	}
	cancel()
	if err := <-canceledErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller err = %v", err)
	}
	// Late waiters hit the cache (the fake clock never advances), so the
	// collector count stays exact however the goroutines interleave.
	close(release)
	wg.Wait()
	close(results)
	for pid := range results {
		if pid != 7 {
			t.Fatalf("pid = %d", pid)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls = %d, want 1", got)
	}
}

func TestListEmptyIsNonNil(t *testing.T) {
	s := NewService(Options{List: func(context.Context) ([]jvm.Info, bool) { return nil, true }})
	infos, err := s.List(context.Background(), false)
	if err != nil || infos == nil {
		t.Fatalf("infos=%#v err=%v", infos, err)
	}
}

func TestListJPSUnavailableIsErrorAndNotCached(t *testing.T) {
	var calls atomic.Int32
	available := false
	s := NewService(Options{List: func(context.Context) ([]jvm.Info, bool) {
		calls.Add(1)
		return nil, available
	}, Clock: clock.NewFake(time.Unix(1000, 0))})
	if _, err := s.List(context.Background(), false); !errors.Is(err, ErrJPSUnavailable) {
		t.Fatalf("err = %v", err)
	}
	available = true
	if infos, err := s.List(context.Background(), false); err != nil || infos == nil || calls.Load() != 2 {
		t.Fatalf("infos=%v err=%v calls=%d", infos, err, calls.Load())
	}
}

func TestListHungCollectorTimesOutAndClearsInflight(t *testing.T) {
	var calls atomic.Int32
	block := make(chan struct{})
	defer close(block)
	list := func(ctx context.Context) ([]jvm.Info, bool) {
		if calls.Add(1) == 1 {
			<-block // ignores ctx entirely
		}
		return []jvm.Info{{PID: 5}}, true
	}
	s := NewService(Options{List: list, ListTimeout: 30 * time.Millisecond, Clock: clock.NewFake(time.Unix(1000, 0))})
	if _, err := s.List(context.Background(), false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung err = %v", err)
	}
	infos, err := s.List(context.Background(), false)
	if err != nil || len(infos) != 1 || infos[0].PID != 5 || calls.Load() != 2 {
		t.Fatalf("recollect: infos=%v err=%v calls=%d", infos, err, calls.Load())
	}
}

func TestListCtxAwareCollectorPartialResultNotCached(t *testing.T) {
	var calls atomic.Int32
	list := func(ctx context.Context) ([]jvm.Info, bool) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return []jvm.Info{{PID: 1}}, true // partial on deadline
		}
		return []jvm.Info{{PID: 2}}, true
	}
	s := NewService(Options{List: list, ListTimeout: 20 * time.Millisecond, Clock: clock.NewFake(time.Unix(1000, 0))})
	if _, err := s.List(context.Background(), false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if infos, err := s.List(context.Background(), false); err != nil || infos[0].PID != 2 {
		t.Fatalf("infos=%v err=%v", infos, err)
	}
}

func TestListPanicReturnsErrorToAllWaiters(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	list := func(context.Context) ([]jvm.Info, bool) {
		if calls.Add(1) == 1 {
			<-release
			panic("boom")
		}
		return []jvm.Info{{PID: 3}}, true
	}
	s := NewService(Options{List: list, Clock: clock.NewFake(time.Unix(1000, 0))})
	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, err := s.List(context.Background(), false); errs <- err }()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	for i := 0; i < n; i++ {
		// A waiter arriving after the panic recollects and succeeds; every
		// waiter of the panicking call sees the panic error.
		if err := <-errs; err != nil && !strings.Contains(err.Error(), "panicked: boom") {
			t.Fatalf("err = %v", err)
		}
	}
	infos, err := s.List(context.Background(), true)
	if err != nil || infos[0].PID != 3 {
		t.Fatalf("after panic: infos=%v err=%v", infos, err)
	}
}

func TestListRPCReturnsWhenConnectionCloses(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s := NewService(Options{List: func(context.Context) ([]jvm.Info, bool) { <-block; return nil, true }})
	done := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		_, err := s.listRPC(context.Background(), &daemon.Request{Method: MethodList, Done: done})
		errc <- err
	}()
	close(done)
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not released by req.Done")
	}
}

func TestFindingsSnapshotTimeoutAndPanic(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	hung := NewService(Options{FindingsTimeout: 20 * time.Millisecond, Catalog: []rules.Rule{},
		Snapshot: func(context.Context) snapshot.Snapshot { <-block; return snapshot.Snapshot{} }})
	if _, err := hung.Findings(context.Background(), 12); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
	panicky := NewService(Options{Catalog: []rules.Rule{},
		Snapshot: func(context.Context) snapshot.Snapshot { panic("boom") }})
	if _, err := panicky.Findings(context.Background(), 12); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Fatalf("panic err = %v", err)
	}
	// Closing the connection releases a findings waiter too.
	slow := NewService(Options{Catalog: []rules.Rule{},
		Snapshot: func(context.Context) snapshot.Snapshot { <-block; return snapshot.Snapshot{} }})
	done := make(chan struct{})
	close(done)
	if _, err := slow.findingsRPC(context.Background(), &daemon.Request{Params: []byte(`{"pid":12}`), Done: done}); !errors.Is(err, context.Canceled) {
		t.Fatalf("done err = %v", err)
	}
}

func TestFindingsUnknownPIDCarriesJPSWarning(t *testing.T) {
	warn := "jvm: jps not found in PATH — JVM discovery unavailable; JVM findings may be incomplete"
	s := NewService(Options{Catalog: []rules.Rule{},
		Snapshot: func(context.Context) snapshot.Snapshot { return snapshot.Snapshot{Warnings: []string{warn}} }})
	_, err := s.Findings(context.Background(), 12)
	if err == nil || !strings.Contains(err.Error(), "pid 12 is not a running JVM") || !strings.Contains(err.Error(), "jps not found") {
		t.Fatalf("err = %v", err)
	}
}

func pidRule(subjects ...string) rules.Rule {
	return rules.Rule{ID: "test-rule", Severity: rules.SeverityLow, MatchFn: func(snapshot.Snapshot) []rules.Finding {
		var out []rules.Finding
		for _, s := range subjects {
			out = append(out, rules.Finding{RuleID: "test-rule", Severity: rules.SeverityLow, Subject: s})
		}
		return out
	}}
}

func twoJVMSnapshot(context.Context) snapshot.Snapshot {
	return snapshot.Snapshot{JVMs: []jvm.Info{{PID: 12}, {PID: 123}}}
}

func TestFindingsFiltersToPID(t *testing.T) {
	catalog := []rules.Rule{pidRule(
		"PID 12",
		"PID 12 (com.example.Main)",
		"PID 12 NMT Internal",
		"PID 12 (com.example.Main) deadlock: a, b",
		"java pid 12",
		"PID 123 (com.example.Other)",
		"PID 123",
		"PID 12x",
		"java pid 123",
		"host memory",
		"",
	)}
	s := NewService(Options{Snapshot: twoJVMSnapshot, Catalog: catalog})
	got, err := s.Findings(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	var subjects []string
	for _, f := range got.Findings {
		subjects = append(subjects, f.Subject)
	}
	want := []string{"PID 12", "PID 12 (com.example.Main)", "PID 12 (com.example.Main) deadlock: a, b", "PID 12 NMT Internal", "java pid 12"}
	slices.Sort(subjects)
	slices.Sort(want)
	if got.PID != 12 || !slices.Equal(subjects, want) {
		t.Fatalf("pid=%d subjects=%q want %q", got.PID, subjects, want)
	}
	other, err := s.Findings(context.Background(), 123)
	if err != nil || len(other.Findings) != 3 {
		t.Fatalf("pid 123: %+v %v", other, err)
	}
}

func TestFindingsWithBuiltinCatalogKeepsOnlyTarget(t *testing.T) {
	// JDK 9 is EOL, so jvm-eol-version fires for both JVMs.
	snap := func(context.Context) snapshot.Snapshot {
		return snapshot.Snapshot{JVMs: []jvm.Info{
			{PID: 12, MainClass: "a.Main", JDKVersion: "9.0.4"},
			{PID: 123, MainClass: "b.Main", JDKVersion: "9.0.4"},
		}}
	}
	s := NewService(Options{Snapshot: snap})
	got, err := s.Findings(context.Background(), 12)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range got.Findings {
		if !strings.HasPrefix(f.Subject, "PID 12 ") {
			t.Fatalf("foreign finding %+v", f)
		}
		found = found || f.RuleID == "jvm-eol-version"
	}
	if !found {
		t.Fatalf("missing jvm-eol-version in %+v", got.Findings)
	}
}

func TestFindingsInvalidAndUnknownPID(t *testing.T) {
	var snaps atomic.Int32
	snap := func(ctx context.Context) snapshot.Snapshot { snaps.Add(1); return twoJVMSnapshot(ctx) }
	s := NewService(Options{Snapshot: snap, Catalog: []rules.Rule{}})
	for _, pid := range []int{0, -1} {
		if _, err := s.Findings(context.Background(), pid); err == nil || err.Error() != "positive pid required" {
			t.Fatalf("pid %d err = %v", pid, err)
		}
	}
	if snaps.Load() != 0 {
		t.Fatal("invalid pid built a snapshot")
	}
	if _, err := s.Findings(context.Background(), 99); err == nil || err.Error() != "pid 99 is not a running JVM" {
		t.Fatalf("unknown pid err = %v", err)
	}
}

func TestMethodsOverDaemonSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spdj-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	RegisterMethods(server, NewService(Options{
		List:     func(context.Context) ([]jvm.Info, bool) { return []jvm.Info{{PID: 12, MainClass: "a.Main"}}, true },
		Snapshot: twoJVMSnapshot,
		Catalog:  []rules.Rule{pidRule("PID 12 (a.Main)", "PID 123 (b.Main)")},
	}))
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
	defer client.Close()

	st, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{MethodList, MethodFindings} {
		if !slices.Contains(st.Methods, m) {
			t.Fatalf("daemon.status methods %v missing %s", st.Methods, m)
		}
	}
	var infos []jvm.Info
	if err := client.Call(ctx, MethodList, map[string]bool{"refresh": true}, &infos); err != nil || len(infos) != 1 || infos[0].MainClass != "a.Main" {
		t.Fatalf("jvm.list: %v %v", infos, err)
	}
	var res FindingsResult
	if err := client.Call(ctx, MethodFindings, map[string]int{"pid": 12}, &res); err != nil || res.PID != 12 || len(res.Findings) != 1 {
		t.Fatalf("jvm.findings: %+v %v", res, err)
	}
	var rpcErr *daemonclient.RPCError
	err = client.Call(ctx, MethodFindings, map[string]int{"pid": 0}, &res)
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
		t.Fatalf("pid 0 err = %v", err)
	}
	err = client.Call(ctx, MethodFindings, map[string]int{"pid": 99}, &res)
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError || !strings.Contains(rpcErr.Message, "not a running JVM") {
		t.Fatalf("pid 99 err = %v", err)
	}
	err = client.Call(ctx, MethodList, "bad", &infos)
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
		t.Fatalf("bad params err = %v", err)
	}
}
