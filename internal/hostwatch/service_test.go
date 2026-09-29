package hostwatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
func TestCurrentRPCHasOnlyNestedSpawn(t *testing.T) {
	svc := NewService(ServiceOptions{SpawnBackend: &fakeSpawnBackend{}})
	svc.current = Sample{Spawn: &SpawnState{ProcsTotal: 7}}
	result, err := svc.currentRPC(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["spawn"]; ok {
		t.Fatalf("duplicated top-level spawn: %s", data)
	}
	var sample Sample
	if err := json.Unmarshal(fields["sample"], &sample); err != nil || sample.Spawn == nil || sample.Spawn.ProcsTotal != 7 {
		t.Fatalf("missing nested spawn: %s: %v", data, err)
	}
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

func TestCollectorJavaArguments(t *testing.T) {
	const java = "/opt/homebrew/Cellar/openjdk@21/21.0.5/bin/java"
	bulk := strings.Join([]string{
		"42 1 1024 95.0 " + java,
		"43 1 2048 5.0 " + java,
		"44 1 4096 3.0 " + java,
		"45 1 512 1.0 " + java,
	}, "\n") + "\n"
	argv := strings.Join([]string{
		"42 " + java + " org.gradle.launcher.daemon.bootstrap.GradleDaemon 9.7.1",
		"43 " + java + " org.jetbrains.kotlin.daemon.KotlinCompileDaemon",
		"44 " + java + " org.gradle.wrapper.GradleWrapperMain",
		"45 " + java + " -jar app.jar",
	}, "\n") + "\n"
	fake := proc.NewFake().
		OnExact("ps", []string{"-eo", "pid=,ppid=,rss=,%cpu=,comm="}, proc.Response{Result: proc.Result{Stdout: []byte(bulk)}}).
		OnExact("ps", []string{"-o", "pid=,args=", "-p", "42,43,44,45"}, proc.Response{Result: proc.Result{Stdout: []byte(argv)}})
	c := &Collector{OS: hostos.Linux, Runner: fake,
		Load:    func() ([3]float64, error) { return [3]float64{}, nil },
		Memory:  func() (string, float64, float64, error) { return "normal", 0, 50, nil },
		Limits:  func(int) map[string]LimitUsage { return map[string]LimitUsage{} },
		Disk:    func() (float64, error) { return 100, nil },
		Thermal: func(context.Context) (bool, error) { return false, nil },
		CPUTime: func() (time.Duration, error) { return 0, nil },
	}
	s, err := c.Collect(context.Background())
	if err != nil || fake.CallCount() != 2 || s.Kinds["gradle"].Count != 2 || s.Kinds["kotlin-daemon"].Count != 1 || s.Kinds["java"].Count != 1 {
		t.Fatalf("calls=%d kinds=%+v err=%v", fake.CallCount(), s.Kinds, err)
	}
}

func TestCollectorJavaArgumentsFailureFallsBack(t *testing.T) {
	fake := proc.NewFake().OnExact("ps", []string{"-eo", "pid=,ppid=,rss=,%cpu=,comm="}, proc.Response{Result: proc.Result{Stdout: []byte("42 1 1024 5.0 /usr/bin/java\n")}})
	log := logger.NewCapture(slog.LevelDebug)
	s := Sample{Kinds: map[string]KindStat{}}
	c := &Collector{OS: hostos.Linux, Runner: fake, Logger: log}
	_, err := c.collectPS(context.Background(), hostos.Linux, &s)
	if err != nil || s.Kinds["java"].Count != 1 || !log.HasMessage("java arguments unavailable") {
		t.Fatalf("kinds=%+v err=%v logs=%+v", s.Kinds, err, log.Records())
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

type blockingSampleStore struct {
	*store.DB
	started, release, written chan struct{}
}

func (s *blockingSampleStore) SaveHostSample(_ context.Context, at time.Time, data []byte) error {
	close(s.started)
	<-s.release
	err := s.DB.SaveHostSample(context.Background(), at, data)
	close(s.written)
	return err
}

func TestServiceCancellationWaitsForStoreWrite(t *testing.T) {
	st := &blockingSampleStore{DB: testDB(t), started: make(chan struct{}), release: make(chan struct{}), written: make(chan struct{})}
	log := logger.NewCapture(slog.LevelDebug)
	svc := NewService(ServiceOptions{Store: st, Collector: &sequenceCollector{samples: []Sample{baseSample(time.Now())}}, Interval: 10 * time.Millisecond, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	select {
	case <-st.started:
	case <-time.After(time.Second):
		t.Fatal("store write did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned before tick finished")
	case <-time.After(30 * time.Millisecond):
	}
	close(st.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not wait for tick")
	}
	select {
	case <-st.written:
	default:
		t.Fatal("Run returned before store write completed")
	}
	if log.HasMessage("watch tick timed out") {
		t.Fatalf("shutdown logged timeout: %+v", log.Records())
	}
}

func TestServiceCollectorTimeoutContinues(t *testing.T) {
	c := &slowCollector{release: make(chan struct{})}
	log := logger.NewCapture(slog.LevelDebug)
	svc := NewService(ServiceOptions{Store: testDB(t), Collector: c, Interval: 15 * time.Millisecond, Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.After(time.Second)
	for !log.HasMessage("watch tick timed out") {
		select {
		case <-deadline:
			t.Fatal("genuine collector timeout not logged")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(c.release)
	for c.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("loop did not continue")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHostwatchLive(t *testing.T) {
	if os.Getenv("SPECTRA_HOSTWATCH_LIVE") != "1" {
		t.Skip("set SPECTRA_HOSTWATCH_LIVE=1 for a real host sample")
	}
	log := logger.NewCapture(slog.LevelDebug)
	s, err := (&Collector{Logger: log}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range log.Records() {
		if record.Msg == "watch process sample unavailable" {
			t.Skipf("real host process sample unavailable: %v", record.Attrs["error"])
		}
	}
	t.Logf("live process kinds: gradle=%d kotlin-daemon=%d java=%d", s.Kinds["gradle"].Count, s.Kinds["kotlin-daemon"].Count, s.Kinds["java"].Count)
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
