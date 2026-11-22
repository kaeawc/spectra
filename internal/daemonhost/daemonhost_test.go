package daemonhost

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
	"testing/fstest"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
	"github.com/kaeawc/spectra/internal/thermal"
	"github.com/kaeawc/spectra/internal/whatswrong"
)

func counting(calls *atomic.Int32) CollectFunc {
	return func(context.Context) (any, error) { return int(calls.Add(1)), nil }
}

func newSvc(f CollectFunc, mutate func(*Options)) (*Service, *clock.Fake) {
	clk := clock.NewFake(time.Unix(1000, 0))
	o := Options{Memory: f, Thermal: f, Power: f, Network: f, Whatswrong: f, Clock: clk}
	if mutate != nil {
		mutate(&o)
	}
	return NewService(o), clk
}

func TestTTLHitAndExpiryPerMethod(t *testing.T) {
	ttls := map[string]time.Duration{
		MethodMemory: DefaultMemoryTTL, MethodThermal: DefaultThermalTTL,
		MethodPower: DefaultPowerTTL, MethodNetwork: DefaultNetworkTTL,
		MethodWhatswrong: DefaultWhatswrongTTL,
	}
	stale := map[string]time.Duration{MethodPower: DefaultPowerStale, MethodWhatswrong: DefaultWhatswrongStale}
	for method, ttl := range ttls {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			s, clk := newSvc(counting(&calls), nil)
			ctx := context.Background()
			a, _ := s.Get(ctx, method, false)
			clk.Advance(ttl - time.Millisecond)
			b, _ := s.Get(ctx, method, false)
			if calls.Load() != 1 || a != b {
				t.Fatalf("calls=%d, want cache hit", calls.Load())
			}
			// Past the TTL and any stale window the caller waits for a new value.
			clk.Advance(time.Millisecond + stale[method])
			c, err := s.Get(ctx, method, false)
			if err != nil || calls.Load() != 2 || c.(int) != 2 {
				t.Fatalf("calls=%d c=%v err=%v, want recollect", calls.Load(), c, err)
			}
		})
	}
}

func TestMethodsHaveSeparateCaches(t *testing.T) {
	var mem, net atomic.Int32
	s, _ := newSvc(nil, func(o *Options) { o.Memory, o.Network = counting(&mem), counting(&net) })
	ctx := context.Background()
	_, _ = s.Get(ctx, MethodMemory, false)
	_, _ = s.Get(ctx, MethodNetwork, false)
	_, _ = s.Get(ctx, MethodMemory, false)
	if mem.Load() != 1 || net.Load() != 1 {
		t.Fatalf("mem=%d net=%d", mem.Load(), net.Load())
	}
}

func TestRefreshBypassesCache(t *testing.T) {
	var calls atomic.Int32
	s, _ := newSvc(counting(&calls), nil)
	_, _ = s.Get(context.Background(), MethodPower, false)
	_, _ = s.Get(context.Background(), MethodPower, true)
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestSingleFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	s, _ := newSvc(func(context.Context) (any, error) {
		calls.Add(1)
		<-release
		return "ok", nil
	}, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, err := s.Get(context.Background(), MethodMemory, i%2 == 0); err != nil || v != "ok" {
				t.Errorf("v=%v err=%v", v, err)
			}
		}()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("calls=%d, want 1 shared collection", calls.Load())
	}
}

func TestTimeoutKillsRunner(t *testing.T) {
	var runErr atomic.Value
	s, _ := newSvc(func(ctx context.Context) (any, error) {
		_, err := proc.Output(ctx, proc.Default, "sleep", "30")
		runErr.Store(err)
		return nil, err
	}, func(o *Options) { o.Timeout = 100 * time.Millisecond })
	start := time.Now()
	_, err := s.Get(context.Background(), MethodThermal, false)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s, sleep was not killed", time.Since(start))
	}
	deadline := time.Now().Add(5 * time.Second)
	for runErr.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runErr.Load() == nil {
		t.Fatal("runner did not return after timeout")
	}
}

func TestTimeoutReleasesWaitersFromIgnoringCollector(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s, _ := newSvc(func(context.Context) (any, error) { <-block; return 1, nil },
		func(o *Options) { o.Timeout = 50 * time.Millisecond })
	if _, err := s.Get(context.Background(), MethodMemory, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestPanicRecovered(t *testing.T) {
	var calls atomic.Int32
	s, _ := newSvc(func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return "fine", nil
	}, nil)
	if _, err := s.Get(context.Background(), MethodMemory, false); err == nil {
		t.Fatal("want panic error")
	}
	if v, err := s.Get(context.Background(), MethodMemory, false); err != nil || v != "fine" {
		t.Fatalf("after panic v=%v err=%v", v, err)
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	s, _ := newSvc(func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return nil, memstate.ErrNotSupported
		}
		return "ok", nil
	}, nil)
	if _, err := s.Get(context.Background(), MethodMemory, false); !errors.Is(err, memstate.ErrNotSupported) {
		t.Fatalf("err = %v", err)
	}
	if v, err := s.Get(context.Background(), MethodMemory, false); err != nil || v != "ok" {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

func TestWaiterReleasedOnDone(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s, _ := newSvc(func(context.Context) (any, error) { <-block; return 1, nil }, nil)
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() {
		_, err := s.methods[MethodMemory].rpc(ctx, &daemon.Request{Done: done})
		res <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(done)
	select {
	case err := <-res:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter not released by req.Done")
	}
	cancel()
}

func TestBadParams(t *testing.T) {
	s, _ := newSvc(counting(new(atomic.Int32)), nil)
	_, err := s.methods[MethodMemory].rpc(context.Background(), &daemon.Request{Params: json.RawMessage(`"bad"`)})
	if err == nil {
		t.Fatal("want invalid params")
	}
}

func TestThermalMatchesCLIShape(t *testing.T) {
	c := &thermal.Collector{OS: hostos.Linux, FS: fstest.MapFS{
		"sys/class/thermal/thermal_zone0/type": {Data: []byte("x86_pkg_temp")},
		"sys/class/thermal/thermal_zone0/temp": {Data: []byte("50000")},
	}}
	got, err := DefaultThermal(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := c.Collect(context.Background())
	want := thermal.WatchReport{Report: rep, Samples: 1}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("got %s want %s", gb, wb)
	}
}

func TestThermalUnsupportedOS(t *testing.T) {
	_, err := DefaultThermal(&thermal.Collector{OS: hostos.Kind(99)})(context.Background())
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestPowerBaselineShape(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	got, err := PowerFrom(func(context.Context) sysinfo.PowerState {
		return sysinfo.PowerState{OnBattery: true, BatteryPct: 85,
			EnergyTopUsers: []sysinfo.EnergyUser{{PID: 501, EnergyImpact: 4.2, Command: "x"}}}
	}, func() time.Time { return now })(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["mode"] != "baseline" || m["interval_ms"].(float64) != 1000 || m["battery"] == nil || m["processes"] == nil {
		t.Fatalf("unexpected payload %s", b)
	}
}

func TestMethodsOverDaemonSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spdh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	svc, _ := newSvc(func(context.Context) (any, error) { return map[string]int{"v": 7}, nil }, nil)
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
	defer client.Close()
	st, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{MethodMemory, MethodThermal, MethodPower, MethodNetwork, MethodWhatswrong} {
		if !slices.Contains(st.Methods, m) {
			t.Fatalf("daemon.status methods %v missing %s", st.Methods, m)
		}
		var out map[string]int
		if err := client.Call(ctx, m, map[string]bool{"refresh": true}, &out); err != nil || out["v"] != 7 {
			t.Fatalf("%s: %v %v", m, out, err)
		}
	}
}

// gated returns a collector whose first call answers at once and whose later
// calls block until release, numbering each call.
func gated(calls *atomic.Int32, release <-chan struct{}) CollectFunc {
	return func(context.Context) (any, error) {
		n := int(calls.Add(1))
		if n > 1 {
			<-release
		}
		return n, nil
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStaleWhileRevalidate(t *testing.T) {
	for _, tc := range []struct {
		method string
		ttl    time.Duration
	}{{MethodPower, DefaultPowerTTL}, {MethodWhatswrong, DefaultWhatswrongTTL}} {
		t.Run(tc.method, func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			s, clk := newSvc(gated(&calls, release), nil)
			ctx := context.Background()
			if v, err := s.Get(ctx, tc.method, false); err != nil || v != 1 {
				t.Fatalf("cold v=%v err=%v", v, err)
			}
			clk.Advance(tc.ttl)
			// The blocked refresh proves stale callers do not wait for it.
			for i := 0; i < 5; i++ {
				if v, err := s.Get(ctx, tc.method, false); err != nil || v != 1 {
					t.Fatalf("stale v=%v err=%v", v, err)
				}
			}
			waitFor(t, func() bool { return calls.Load() == 2 })
			close(release)
			waitFor(t, func() bool { v, _ := s.Get(ctx, tc.method, false); return v == 2 })
			if calls.Load() != 2 {
				t.Fatalf("calls=%d, want one background refresh", calls.Load())
			}
		})
	}
}

func TestStaleRefreshFailureNotCached(t *testing.T) {
	var calls atomic.Int32
	s, clk := newSvc(func(context.Context) (any, error) {
		switch calls.Add(1) {
		case 2:
			return nil, errors.New("top failed")
		default:
			return int(calls.Load()), nil
		}
	}, nil)
	ctx := context.Background()
	_, _ = s.Get(ctx, MethodWhatswrong, false)
	clk.Advance(DefaultWhatswrongTTL)
	if v, err := s.Get(ctx, MethodWhatswrong, false); err != nil || v != 1 {
		t.Fatalf("stale v=%v err=%v", v, err)
	}
	m := s.methods[MethodWhatswrong]
	waitFor(t, func() bool { m.mu.Lock(); defer m.mu.Unlock(); return calls.Load() == 2 && m.inflight == nil })
	// The failed refresh left the old value in place and the next stale call
	// tries again.
	if v, err := s.Get(ctx, MethodWhatswrong, false); err != nil || v != 1 {
		t.Fatalf("after failure v=%v err=%v", v, err)
	}
	waitFor(t, func() bool { v, _ := s.Get(ctx, MethodWhatswrong, false); return v == 3 })
}

func TestStaleBeyondWindowWaits(t *testing.T) {
	var calls atomic.Int32
	s, clk := newSvc(counting(&calls), nil)
	ctx := context.Background()
	_, _ = s.Get(ctx, MethodPower, false)
	clk.Advance(DefaultPowerTTL + DefaultPowerStale)
	if v, err := s.Get(ctx, MethodPower, false); err != nil || v != 2 {
		t.Fatalf("v=%v err=%v, want a fresh collection", v, err)
	}
}

func TestRefreshForcesCollectionWhenStaleServed(t *testing.T) {
	var calls atomic.Int32
	s, _ := newSvc(counting(&calls), nil)
	ctx := context.Background()
	_, _ = s.Get(ctx, MethodWhatswrong, false)
	if v, err := s.Get(ctx, MethodWhatswrong, true); err != nil || v != 2 {
		t.Fatalf("v=%v err=%v, want forced collection", v, err)
	}
}

func TestWhatswrongWaiterReleasedOnDone(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s, _ := newSvc(func(context.Context) (any, error) { <-block; return 1, nil }, nil)
	done := make(chan struct{})
	res := make(chan error, 1)
	go func() {
		_, err := s.methods[MethodWhatswrong].rpc(context.Background(), &daemon.Request{Done: done, Params: json.RawMessage(`{"refresh":true}`)})
		res <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(done)
	select {
	case err := <-res:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiter not released by req.Done")
	}
}

func TestPowerTimeoutBelowClientBudget(t *testing.T) {
	s := NewService(Options{})
	if got := s.methods[MethodPower].timeout; got != DefaultPowerTimeout {
		t.Fatalf("power timeout = %s", got)
	}
	if got := NewService(Options{Timeout: time.Second}).methods[MethodPower].timeout; got != time.Second {
		t.Fatalf("override timeout = %s", got)
	}
}

func TestWhatswrongMatchesCLIShape(t *testing.T) {
	deps := whatswrong.Deps{
		Load:  func(context.Context) sysload.Load { return sysload.Load{MemoryPressure: "critical", SwapUsedMB: 9000} },
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{ThermalPressure: "serious"} },
		Procs: func(context.Context) []process.Info { return []process.Info{{PID: 7, Command: "hog", CPUPct: 99}} },
		Now:   func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Connect: whatswrong.LocalWatch(fakeWatch{alerts: []hostwatch.Alert{{Title: "Disk nearly full", Severity: "warning"}},
			samples: []hostwatch.Sample{{At: time.Unix(1_700_000_000, 0).UTC(), Load1: 2}}}),
	}
	got, err := WhatswrongFrom(deps)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(whatswrong.Collect(context.Background(), deps))
	if string(gb) != string(wb) {
		t.Fatalf("got %s want %s", gb, wb)
	}
	var m map[string]any
	_ = json.Unmarshal(gb, &m)
	if m["causes"] == nil || m["signals"] == nil || m["daemon_alerts"] == nil || m["trend"] == nil {
		t.Fatalf("unexpected payload %s", gb)
	}
}

type fakeWatch struct {
	alerts  []hostwatch.Alert
	samples []hostwatch.Sample
}

func (f fakeWatch) Current() (hostwatch.Sample, []hostwatch.Alert) {
	return hostwatch.Sample{}, f.alerts
}
func (f fakeWatch) Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error) {
	return f.samples, nil
}
