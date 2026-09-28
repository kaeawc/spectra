package hostwatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/store"
)

type sequenceCollector struct {
	samples []Sample
	i       int
	metrics *metrics.Collector
}

func (c *sequenceCollector) Collect(context.Context) (Sample, error) {
	s := c.samples[c.i]
	c.i++
	if c.metrics != nil {
		c.metrics.Add(metrics.Sample{TakenAt: s.At, PID: 42, RSSKiB: 123, CPUPct: 90})
	}
	return s, nil
}
func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func TestCollectorSinglePSAndProcessMetrics(t *testing.T) {
	m := metrics.NewCollector()
	fake := proc.NewFake().OnExact("ps", []string{"-eo", "pid=,ppid=,rss=,%cpu=,comm="}, proc.Response{Result: proc.Result{Stdout: []byte("42 1 1024 95.0 /usr/bin/gradle\n43 1 2048 5.0 node\n")}})
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := &Collector{OS: hostos.Linux, Runner: fake, Clock: clock.NewFake(at), NCPU: func() int { return 8 }, Load: func() ([3]float64, error) { return [3]float64{1, 2, 3}, nil }, Memory: func() (string, float64, float64, error) { return "normal", 0, 50, nil }, Limits: func(int) map[string]LimitUsage { return map[string]LimitUsage{} }, Disk: func() (float64, error) { return 100, nil }, Thermal: func(context.Context) (bool, error) { return false, nil }, CPUTime: func() (time.Duration, error) { return 0, nil }, Metrics: m}
	s, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fake.CallCount() != 1 || s.Kinds["gradle"].Count != 1 || len(s.TopCPU) != 2 || len(m.Recent(42, 10)) != 1 || s.NCPU != 8 {
		t.Fatalf("sample %+v calls %d", s, fake.CallCount())
	}
}

func TestCollectorPersistsHostSignalsWhenPSUnavailable(t *testing.T) {
	log := logger.NewCapture(slog.LevelDebug)
	c := &Collector{
		OS: hostos.Linux, Runner: proc.NewFake(), Logger: log,
		Load:    func() ([3]float64, error) { return [3]float64{2, 1, 1}, nil },
		Memory:  func() (string, float64, float64, error) { return "warn", 512, 10, nil },
		Limits:  func(int) map[string]LimitUsage { return map[string]LimitUsage{} },
		Disk:    func() (float64, error) { return 50, nil },
		Thermal: func(context.Context) (bool, error) { return false, nil },
	}
	s, err := c.Collect(context.Background())
	if err != nil || s.Load1 != 2 || s.MemoryPressure != "warn" || !log.HasMessage("process sample unavailable") {
		t.Fatalf("partial sample %+v, %v", s, err)
	}
}
func TestServiceTickSelfGuard(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var ss []Sample
	for i := 0; i < 20; i++ {
		s := baseSample(at.Add(time.Duration(i) * time.Second))
		if i < 10 {
			s.SelfCPUPct = 3
		}
		ss = append(ss, s)
	}
	svc := NewService(ServiceOptions{Store: testDB(t), Collector: &sequenceCollector{samples: ss}, Interval: time.Second})
	for i := 0; i < 10; i++ {
		if err := svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if svc.interval != 2*time.Second {
		t.Fatalf("backoff %v", svc.interval)
	}
	for i := 10; i < 20; i++ {
		if err := svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if svc.interval != time.Second {
		t.Fatalf("restore %v", svc.interval)
	}
}

type slowCollector struct {
	calls   atomic.Int32
	release chan struct{}
}

func (c *slowCollector) Collect(context.Context) (Sample, error) {
	c.calls.Add(1)
	<-c.release
	return baseSample(time.Now()), nil
}
func TestServiceSkipsSlowCollector(t *testing.T) {
	c := &slowCollector{release: make(chan struct{})}
	svc := NewService(ServiceOptions{Store: testDB(t), Collector: c, Interval: 10 * time.Millisecond, Logger: logger.NewCapture(slog.LevelDebug)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = svc.Run(ctx) }()
	time.Sleep(55 * time.Millisecond)
	if c.calls.Load() != 1 {
		t.Fatalf("overlapping collectors: %d", c.calls.Load())
	}
	cancel()
	close(c.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run stuck")
	}
}
func TestRPCSubscriptionAndHistory(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spw-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := baseSample(at)
	s.DataFreeGB = 4
	m := metrics.NewCollector()
	svc := NewService(ServiceOptions{Store: testDB(t), Metrics: m, Collector: &sequenceCollector{samples: []Sample{s}, metrics: m}})
	server := daemon.New(daemon.Options{Paths: paths})
	RegisterMethods(server, svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	var client *daemonclient.Client
	for i := 0; i < 100; i++ {
		client, err = daemonclient.Dial(context.Background(), paths.Socket)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	events := make(chan Event, 1)
	client.OnNotification(func(method string, raw json.RawMessage) {
		if method == "alerts.event" {
			var e Event
			_ = json.Unmarshal(raw, &e)
			events <- e
		}
	})
	var sub map[string]bool
	if err = client.Call(context.Background(), "alerts.subscribe", map[string]bool{"samples": true}, &sub); err != nil || !sub["subscribed"] {
		t.Fatalf("subscribe %v %+v", err, sub)
	}
	if err = svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var samples []Sample
	if err = client.Call(context.Background(), "watch.samples", nil, &samples); err != nil || len(samples) != 1 {
		t.Fatalf("samples %+v %v", samples, err)
	}
	select {
	case e := <-events:
		if e.Type != "fired" || e.Alert.Key != "disk.free" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing alert event")
	}
	var history []metrics.Sample
	if err = client.Call(context.Background(), "process.history", map[string]int{"pid": 42}, &history); err != nil || len(history) != 1 || history[0].RSSKiB != 123 {
		t.Fatalf("history %+v %v", history, err)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server stuck")
	}
}
