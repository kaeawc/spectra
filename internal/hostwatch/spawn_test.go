package hostwatch

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/store"
)

type fakeSpawnBackend struct {
	mu                   sync.Mutex
	processes            []SpawnProcess
	uidLimit, totalLimit int
	argv                 map[int]string
	argvCalls            int
}

func (f *fakeSpawnBackend) List() ([]SpawnProcess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]SpawnProcess(nil), f.processes...), nil
}
func (f *fakeSpawnBackend) Limits() (int, int) { return f.uidLimit, f.totalLimit }
func (f *fakeSpawnBackend) Argv(pid int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.argvCalls++
	return f.argv[pid], nil
}
func (f *fakeSpawnBackend) set(processes []SpawnProcess) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processes = processes
}

func spawnProcesses(first, n, ppid int, comm string) []SpawnProcess {
	out := make([]SpawnProcess, n)
	for i := range out {
		out[i] = SpawnProcess{PID: first + i, PPID: ppid, UID: 501, Comm: comm}
	}
	return out
}

func TestSpawnRateAndThresholds(t *testing.T) {
	cfg := DefaultConfig()
	tracker := &spawnTracker{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := spawnProcesses(100, 10, 1, "base")
	state := tracker.update(at, base, 501, 1000, 1000)
	if state.Delta != 0 || state.NewPerSec != 0 || len(evaluateSpawn(cfg, tracker, state)) != 0 {
		t.Fatalf("first probe: %+v", state)
	}
	first := append(append([]SpawnProcess(nil), base...), spawnProcesses(1, 40, 1, "new")...)
	state = tracker.update(at.Add(time.Second), first, 501, 1000, 1000)
	if state.Delta != 40 || state.NewPerSec != 40 || len(evaluateSpawn(cfg, tracker, state)) != 0 {
		t.Fatalf("first warning probe: %+v", state)
	}
	second := append(first, spawnProcesses(100000, 40, 1, "new")...)
	state = tracker.update(at.Add(2*time.Second), second, 501, 1000, 1000)
	conditions := evaluateSpawn(cfg, tracker, state)
	if len(conditions) != 1 || conditions[0].Key != "spawn.rate" || conditions[0].Severity != "warn" {
		t.Fatalf("second warning probe: %+v", conditions)
	}
	wrapped := append(append([]SpawnProcess(nil), second...), spawnProcesses(1000000, 120, 1, "new")...)
	state = tracker.update(at.Add(3*time.Second), wrapped, 501, 1000, 1000)
	conditions = evaluateSpawn(cfg, tracker, state)
	if len(conditions) != 1 || conditions[0].Severity != "critical" || state.Delta != 120 {
		t.Fatalf("critical probe: %+v %+v", state, conditions)
	}
	// A wrapped PID that remains in both snapshots is not a new set member.
	state = tracker.update(at.Add(4*time.Second), wrapped, 501, 1000, 1000)
	if state.Delta != 0 || state.NewPerSec != 0 {
		t.Fatalf("stable PID set: %+v", state)
	}
	wrap := &spawnTracker{}
	wrap.update(at, spawnProcesses(999990, 5, 1, "old"), 501, 1000, 1000)
	state = wrap.update(at.Add(time.Second), spawnProcesses(1, 5, 1, "wrapped"), 501, 1000, 1000)
	if state.Delta != 5 || state.NewPerSec != 5 {
		t.Fatalf("PID wrap: %+v", state)
	}
}

func TestSpawnUIDLimitsAndClearCount(t *testing.T) {
	cfg := DefaultConfig()
	tracker := &spawnTracker{}
	e := NewEngine(nil)
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, n := range []int{4, 7, 3, 3, 3} {
		state := tracker.update(at.Add(time.Duration(i)*time.Second), spawnProcesses(10, n, 1, "test"), 501, 10, 100)
		conditions := evaluateSpawn(cfg, tracker, state)
		events := e.EvaluateSpawn(state.At, conditions)
		want := []string{"fired", "updated", "", "", "resolved"}[i]
		if want == "" && len(events) == 0 {
			continue
		}
		if len(events) != 1 || events[0].Type != want || events[0].Alert.Key != "procs.uid" {
			t.Fatalf("probe %d: %+v", i, events)
		}
	}
	if spawnPctSeverity(8, 10, 50, 80) != "critical" || spawnPctSeverity(5, 10, 50, 80) != "warn" || spawnPctSeverity(100, 0, 50, 80) != "" || spawnPctSeverity(50, 100, 50, 80) != "warn" {
		t.Fatal("percentage boundary or unlimited limit")
	}
}

func TestSpawnAttributionAndDetail(t *testing.T) {
	b := &fakeSpawnBackend{argv: map[int]string{10: strings.Repeat("a", 250), 20: "other"}}
	processes := []SpawnProcess{{PID: 10, UID: 501, Comm: "go"}, {PID: 20, UID: 501, Comm: "shell"}}
	processes = append(processes, spawnProcesses(100, 7, 10, "krit.test")...)
	processes = append(processes, spawnProcesses(200, 3, 20, "git")...)
	s := SpawnState{UID: 501, ProcsUID: len(processes), NewPerSec: 120}
	attributeSpawn(&s, processes, b, true)
	if len(s.TopParents) != 2 || s.TopParents[0].PID != 10 || s.TopParents[0].ChildCount != 7 || len([]rune(s.TopParents[0].Argv)) != 200 || s.TopCommands[0].Comm != "krit.test" || b.argvCalls != 2 {
		t.Fatalf("attribution: %+v", s)
	}
	if len([]rune(spawnDetail(s))) > 300 {
		t.Fatal("detail too long")
	}
}

type countingSpawnStore struct {
	*store.DB
	writes atomic.Int32
}

func (s *countingSpawnStore) SaveHostSample(ctx context.Context, at time.Time, data []byte) error {
	s.writes.Add(1)
	return s.DB.SaveHostSample(ctx, at, data)
}

func TestSpawnPersistsOnlyFireAndEscalation(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	b := &fakeSpawnBackend{uidLimit: 10, totalLimit: 1000}
	st := &countingSpawnStore{DB: testDB(t)}
	svc := NewService(ServiceOptions{Store: st, SpawnBackend: b, UID: func() int { return 501 }, Clock: clk})
	for i, n := range []int{3, 4, 4, 7, 7, 3, 3, 3} {
		b.set(spawnProcesses(100, n, 1, "test"))
		if err := svc.TickSpawn(context.Background()); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Second)
		want := []int32{0, 1, 1, 2, 2, 2, 2, 2}[i]
		if st.writes.Load() != want {
			t.Fatalf("probe %d wrote %d samples, want %d", i, st.writes.Load(), want)
		}
		if i == 5 {
			current, _ := svc.Current()
			if current.Spawn == nil || len(current.Spawn.TopParents) == 0 {
				t.Fatal("active alert lost parent attribution during clear grace")
			}
		}
	}
	if b.argvCalls != 2 {
		t.Fatalf("argv reads = %d, want one per firing/escalation", b.argvCalls)
	}
	sample, alerts := svc.Current()
	if sample.Spawn == nil || sample.Spawn.ProcsUID != 3 || len(alerts) != 0 || len(svc.spawnTracker.probes) != 8 {
		t.Fatalf("current: %+v alerts: %+v", sample.Spawn, alerts)
	}
	rows, err := st.ListHostSamples(context.Background(), time.Time{}, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %d: %v", len(rows), err)
	}
	var saved Sample
	if err := json.Unmarshal(rows[0].JSON, &saved); err != nil || saved.Spawn == nil || saved.Spawn.ProcsUID != 7 {
		t.Fatalf("saved spawn: %+v %v", saved.Spawn, err)
	}
}

type blockedNotifier struct{ release chan struct{} }

func (n blockedNotifier) Notify(ctx context.Context, _ Alert) error {
	select {
	case <-n.release:
	case <-ctx.Done():
	}
	return nil
}

func TestSpawnAlertWhileSlowCollectorBlockedAndNotifierNonblocking(t *testing.T) {
	b := &fakeSpawnBackend{uidLimit: 10, totalLimit: 1000, processes: spawnProcesses(100, 7, 1, "test")}
	slow := &slowCollector{release: make(chan struct{})}
	notify := blockedNotifier{release: make(chan struct{})}
	svc := NewService(ServiceOptions{Store: testDB(t), Collector: slow, SpawnBackend: b, UID: func() int { return 501 }, Notifier: notify, Interval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		_, alerts := svc.Current()
		if slow.calls.Load() > 0 && len(alerts) == 1 && alerts[0].Key == "procs.uid" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fast alert missing while slow collector blocked")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(notify.release)
	cancel()
	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSpawnProbeDoesNotWaitForNotifier(t *testing.T) {
	b := &fakeSpawnBackend{uidLimit: 10, totalLimit: 1000, processes: spawnProcesses(100, 7, 1, "test")}
	notify := blockedNotifier{release: make(chan struct{})}
	svc := NewService(ServiceOptions{Store: testDB(t), SpawnBackend: b, UID: func() int { return 501 }, Notifier: notify})
	done := make(chan error, 1)
	go func() { done <- svc.TickSpawn(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("probe waited for blocked notifier")
	}
	close(notify.release)
}

func TestSlowSampleUsesSpawnUIDCount(t *testing.T) {
	b := &fakeSpawnBackend{uidLimit: 10, totalLimit: 1000, processes: spawnProcesses(100, 4, 1, "test")}
	svc := NewService(ServiceOptions{Store: testDB(t), Collector: &sequenceCollector{samples: []Sample{baseSample(time.Now())}}, SpawnBackend: b, UID: func() int { return 501 }})
	if err := svc.TickSpawn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := svc.Current()
	if s.Limits["procs_per_uid"].Current != 4 || s.Spawn == nil || s.Spawn.ProcsUID != 4 {
		t.Fatalf("slow sample: %+v", s)
	}
}

func TestSpawnConfigValidation(t *testing.T) {
	cfg, err := LoadConfig("watch.yml", func(string) ([]byte, error) {
		return []byte("spawn:\n  interval: 500ms\n  rate_warn: -1\n  uid_critical_pct: 0\n"), nil
	})
	if err == nil || cfg.Spawn.Interval != 5*time.Second || cfg.Spawn.RateWarn != 40 || cfg.Spawn.UIDCriticalPct != 70 {
		t.Fatalf("config %+v, err %v", cfg.Spawn, err)
	}
	cfg, err = LoadConfig("watch.yml", func(string) ([]byte, error) {
		return []byte("spawn:\n  interval: 1s\n  rate_warn: 50\n  rate_critical: 130\n"), nil
	})
	if err != nil || cfg.Spawn.Interval != time.Second || cfg.Spawn.RateWarn != 50 || cfg.Spawn.RateCritical != 130 {
		t.Fatalf("valid config %+v, err %v", cfg.Spawn, err)
	}
}

func TestDarwinSpawnLive(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("SPECTRA_HOSTWATCH_LIVE") != "1" {
		t.Skip("requires macOS and SPECTRA_HOSTWATCH_LIVE=1")
	}
	b := newSpawnBackend()
	start := time.Now()
	rows, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	uid := os.Getuid()
	count := 0
	for _, p := range rows {
		if p.UID == uid {
			count++
		}
	}
	t.Logf("procs_total=%d procs_uid=%d probe_ms=%.2f", len(rows), count, float64(time.Since(start))/float64(time.Millisecond))
}
