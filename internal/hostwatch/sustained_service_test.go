package hostwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/store"
)

type scriptRunner struct {
	ps        func() string
	sampleErr bool
	sampleOut string
	calls     []string
}

func (r *scriptRunner) Run(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
	r.calls = append(r.calls, cmd.Name)
	switch cmd.Name {
	case "ps":
		if len(cmd.Args) > 0 && cmd.Args[0] == "-L" {
			return proc.Result{Stdout: []byte("412 80.0 worker\n413 1.0 idle\n")}, nil
		}
		return proc.Result{Stdout: []byte(r.ps())}, nil
	case "sample":
		if r.sampleErr {
			return proc.Result{ExitCode: 1, Stderr: []byte("sample: requires root")}, nil
		}
		return proc.Result{Stdout: []byte(r.sampleOut)}, nil
	}
	return proc.Result{ExitCode: -1}, errors.New("unexpected " + cmd.Name)
}

func (r *scriptRunner) count(name string) int {
	n := 0
	for _, c := range r.calls {
		if c == name {
			n++
		}
	}
	return n
}

func fmtEtime(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d-%02d:%02d:%02d", s/86400, s/3600%24, s/60%60, s%60)
}

type countStore struct {
	SampleStore
	upserts int
}

func (c *countStore) UpsertAlert(ctx context.Context, row store.AlertRow) error {
	c.upserts++
	return c.SampleStore.UpsertAlert(ctx, row)
}

var svcT0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func minutes(m int) time.Time { return svcT0.Add(time.Duration(m) * time.Minute) }

func firingKeys(t *testing.T, svc *Service) []Alert {
	t.Helper()
	alerts, err := svc.Alerts(context.Background(), "firing", 10)
	if err != nil {
		t.Fatal(err)
	}
	return alerts
}

func TestSustainedSampleFailureDetailAndBackoff(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	r := &scriptRunner{sampleErr: true, ps: func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(now.Sub(start)))
	}}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 50; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	alerts := firingKeys(t, svc)
	if len(alerts) != 1 || !strings.Contains(alerts[0].Detail, "sample unavailable (needs root: run `sudo sample 412 10`)") {
		t.Fatalf("alerts: %+v", alerts)
	}
	// One initial attempt plus one retry after the 30 minute backoff.
	if got := r.count("sample"); got != 2 {
		t.Fatalf("sample attempts = %d, want 2", got)
	}
}

func TestSustainedOneSamplePerTick(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	r := &scriptRunner{sampleOut: "Sort by top of stack, same collapsing path:\n  spin 1\n\n", ps: func() string {
		age := fmtEtime(now.Sub(start))
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n413 1 90.0 %s /usr/libexec/OSLogService\n", age, age)
	}}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 30; m++ {
		now = minutes(m)
		before := r.count("sample")
		svc.TickSustained(context.Background(), now)
		if r.count("sample")-before > 1 {
			t.Fatalf("minute %d ran %d samples in one tick", m, r.count("sample")-before)
		}
	}
	if r.count("sample") != 2 {
		t.Fatalf("expected both findings sampled eventually, got %d", r.count("sample"))
	}
	for _, a := range firingKeys(t, svc) {
		if !strings.Contains(a.Detail, "sample top of stack: spin 1") {
			t.Errorf("%s lacks summary: %s", a.Key, a.Detail)
		}
	}
}

func TestSustainedPersistRefreshEveryFiveMinutes(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(now.Sub(start)))
	}}
	cs := &countStore{SampleStore: testDB(t)}
	svc := NewService(ServiceOptions{Store: cs, Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	fired := -1
	var base int
	for m := 0; m <= 40; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
		switch {
		case fired < 0 && cs.upserts > 0:
			fired, base = m, cs.upserts
		case fired >= 0 && m < fired+5 && cs.upserts != base:
			t.Fatalf("minute %d wrote before the refresh interval (%d > %d)", m, cs.upserts, base)
		case fired >= 0 && m == fired+5 && cs.upserts != base+1:
			t.Fatalf("minute %d: refresh did not write (%d, base %d)", m, cs.upserts, base)
		}
	}
	if fired < 0 {
		t.Fatal("never fired")
	}
}

func TestSustainedProcessExitResolves(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	alive := true
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		if !alive {
			return "1 0 0.0 9-00:00:00 /sbin/launchd\n"
		}
		return fmt.Sprintf("412 1 90.0 %s /usr/libexec/diagnosticd\n", fmtEtime(now.Sub(start)))
	}}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 20; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	if len(firingKeys(t, svc)) != 1 {
		t.Fatal("expected firing alert")
	}
	alive = false
	for m := 21; m <= 23; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	if got := firingKeys(t, svc); len(got) != 0 {
		t.Fatalf("exited process still firing: %+v", got)
	}
}

func TestSustainedSleepGapKeepsAlertID(t *testing.T) {
	start := svcT0.Add(-5 * time.Hour)
	var now time.Time
	cpu := 90.0
	r := &scriptRunner{sampleOut: "x", ps: func() string {
		return fmt.Sprintf("412 1 %.1f %s /usr/libexec/diagnosticd\n", cpu, fmtEtime(now.Sub(start)))
	}}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	for m := 0; m <= 20; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	before := firingKeys(t, svc)
	if len(before) != 1 {
		t.Fatalf("expected firing alert: %+v", before)
	}
	// The host sleeps for two hours, then the daemon is still busy.
	for m := 140; m <= 145; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	after := firingKeys(t, svc)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("alert changed across sleep: before %+v after %+v", before, after)
	}
	// Fresh samples show the load is gone: the carried alert is disproved.
	cpu = 1
	for m := 146; m <= 160; m++ {
		now = minutes(m)
		svc.TickSustained(context.Background(), now)
	}
	if got := firingKeys(t, svc); len(got) != 0 {
		t.Fatalf("carried alert never resolved: %+v", got)
	}
}

// linuxHost simulates /proc and ps for the Linux delta path.
type linuxHost struct {
	now   time.Time
	start time.Time
	ticks int64 // cumulative utime+stime in clock ticks
}

func (h *linuxHost) ps() string {
	// %cpu is the lifetime average, deliberately misleading.
	return fmt.Sprintf("412 1 %s %s diagnosticd\n", h.lifetime(), fmtEtime(h.now.Sub(h.start)))
}

func (h *linuxHost) lifetime() string {
	return fmt.Sprintf("%.1f", float64(h.ticks)/100/h.now.Sub(h.start).Seconds()*100)
}

func (h *linuxHost) read(p string) ([]byte, error) {
	if p != "/proc/412/stat" {
		return nil, errors.New("no such file")
	}
	return []byte(fmt.Sprintf("412 (diagnosticd) S 1 0 0 0 0 0 0 0 0 0 %d 0 0 0", h.ticks)), nil
}

func newLinuxService(t *testing.T, h *linuxHost) *Service {
	t.Helper()
	r := &scriptRunner{ps: h.ps, sampleOut: ""}
	return NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Linux, ReadFile: h.read, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
}

func TestSustainedLinuxNewlyPinnedOldDaemonFires(t *testing.T) {
	// Ten days of near-idle history keep the lifetime average under 1%, which
	// the old %cpu signal would never lift above the threshold.
	h := &linuxHost{start: svcT0.Add(-240 * time.Hour), ticks: 100 * 3600}
	svc := newLinuxService(t, h)
	for m := 0; m <= 25; m++ {
		h.now = minutes(m)
		if m > 0 {
			h.ticks += 100 * 60 * 90 / 100 // 90% of one CPU for a minute
		}
		svc.TickSustained(context.Background(), h.now)
	}
	alerts := firingKeys(t, svc)
	if len(alerts) != 1 || !strings.Contains(alerts[0].Detail, "averaged 90% CPU") || !strings.Contains(alerts[0].Detail, "top -H -p 412") {
		t.Fatalf("alerts: %+v", alerts)
	}
}

func TestSustainedLinuxIdleAfterBusyResolves(t *testing.T) {
	// Busy for a day: lifetime average stays high after the load stops.
	h := &linuxHost{start: svcT0.Add(-24 * time.Hour), ticks: 100 * 86400 * 90 / 100}
	svc := newLinuxService(t, h)
	busy := true
	for m := 0; m <= 60; m++ {
		h.now = minutes(m)
		if busy && m > 0 {
			h.ticks += 100 * 60 * 90 / 100
		}
		if m == 20 {
			if len(firingKeys(t, svc)) != 1 {
				t.Fatal("expected firing while busy")
			}
			busy = false
		}
		svc.TickSustained(context.Background(), h.now)
	}
	if got := firingKeys(t, svc); len(got) != 0 {
		t.Fatalf("idle daemon still alerting from lifetime average: %+v", got)
	}
	if lifetime := h.lifetime(); lifetime < "50" {
		t.Fatalf("test premise broken, lifetime avg %s", lifetime)
	}
}

func TestSustainedLinuxFirstSightingYieldsNoSample(t *testing.T) {
	h := &linuxHost{start: svcT0.Add(-time.Hour), ticks: 1000}
	st := newSustainedState(DefaultSustainedConfig(), nil, h.read, hostos.Linux)
	h.now = svcT0
	procs := []ProcObs{{PID: 412, Name: "diagnosticd", CPUPct: 99, Started: h.start}}
	if got := st.cpuObservations(procs, h.now); len(got) != 0 {
		t.Fatalf("first sighting produced %+v", got)
	}
	h.now = svcT0.Add(10 * time.Second)
	h.ticks += 500 // 5s of CPU in 10s
	got := st.cpuObservations(procs, h.now)
	if len(got) != 1 || got[0].CPUPct < 49.9 || got[0].CPUPct > 50.1 {
		t.Fatalf("delta cpu: %+v", got)
	}
	// Same PID with a new start time is a different process.
	h.now = svcT0.Add(20 * time.Second)
	procs[0].Started = h.start.Add(time.Hour)
	if got := st.cpuObservations(procs, h.now); len(got) != 0 {
		t.Fatalf("PID reuse produced %+v", got)
	}
}

func TestValidateWatchIntervalAndTrackerGap(t *testing.T) {
	c := DefaultConfig()
	c.Sustained.Window = 20 * time.Second
	if err := c.ValidateWatchInterval(15 * time.Second); err == nil || c.Sustained.Window != 4*time.Minute {
		t.Fatalf("window not widened: %v %v", err, c.Sustained.Window)
	}
	if err := c.ValidateWatchInterval(15 * time.Second); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
	cfg := DefaultSustainedConfig()
	if g := NewSustainedTracker(cfg).maxGap(); g != 5*time.Minute {
		t.Fatalf("default gap %v", g)
	}
	cfg.MaxGap = 12 * time.Minute
	if g := NewSustainedTracker(cfg).maxGap(); g != 12*time.Minute {
		t.Fatalf("interval gap %v", g)
	}
}

func TestSustainedTrackerCarriesFiringAcrossGap(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	started := at(-300)
	last, _ := runSteps(tr, "diagnosticd", started, ramp(0, 20, 0.25, 90))
	if len(last) != 1 {
		t.Fatal("expected finding before gap")
	}
	since := last[0].Since
	got := tr.Observe(at(200), []ProcObs{obs(10, "diagnosticd", 2, started)})
	if len(got) != 1 || !got[0].Since.Equal(since) {
		t.Fatalf("carried finding lost: %+v", got)
	}
	// Not disproved by fewer than the minimum fresh samples, then cleared.
	for i := 1; i <= 8; i++ {
		got = tr.Observe(at(200+float64(i)*0.25), []ProcObs{obs(10, "diagnosticd", 2, started)})
	}
	if len(got) != 0 {
		t.Fatalf("fresh idle samples did not clear: %+v", got)
	}
}

func TestSustainedClearsWhenWindowHoldsFewerThanFourSamples(t *testing.T) {
	cfg := DefaultSustainedConfig()
	cfg.Window, cfg.Interval = 3*time.Minute, time.Minute
	tr := NewSustainedTracker(cfg)
	started := at(-300)
	var last []SustainedFinding
	for m := 0; m <= 6; m++ {
		last = tr.Observe(at(float64(m)), []ProcObs{obs(10, "diagnosticd", 90, started)})
	}
	if len(last) != 1 {
		t.Fatalf("expected finding: %+v", last)
	}
	for m := 7; m <= 14; m++ {
		last = tr.Observe(at(float64(m)), []ProcObs{obs(10, "diagnosticd", 1, started)})
	}
	if len(last) != 0 {
		t.Fatalf("alert stuck with window < 4 intervals: %+v", last)
	}
	// A carried entry (gap reset) must also leave the carried state.
	tr = NewSustainedTracker(cfg)
	for m := 0; m <= 6; m++ {
		tr.Observe(at(float64(m)), []ProcObs{obs(10, "diagnosticd", 90, started)})
	}
	for m := 60; m <= 70; m++ {
		last = tr.Observe(at(float64(m)), []ProcObs{obs(10, "diagnosticd", 1, started)})
	}
	if len(last) != 0 || len(tr.entries) != 0 {
		t.Fatalf("carried entry never cleared: %+v %+v", last, tr.entries)
	}
}

func TestSustainedTrackerSkipKeepsState(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	started := at(-300)
	last, _ := runSteps(tr, "diagnosticd", started, ramp(0, 20, 0.25, 90))
	if len(last) != 1 {
		t.Fatal("expected finding")
	}
	skip := obs(10, "diagnosticd", 0, started)
	skip.Skip = true
	got := tr.Observe(at(20.25), []ProcObs{skip})
	if len(got) != 1 || !got[0].Since.Equal(last[0].Since) {
		t.Fatalf("skip dropped state: %+v", got)
	}
}

func TestSustainedLinuxTransientStatFailureKeepsAlert(t *testing.T) {
	h := &linuxHost{start: svcT0.Add(-240 * time.Hour), ticks: 100 * 3600}
	failing := false
	r := &scriptRunner{ps: h.ps}
	svc := NewService(ServiceOptions{Store: testDB(t), Runner: r, OS: hostos.Linux, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{},
		ReadFile: func(p string) ([]byte, error) {
			if failing {
				return nil, errors.New("transient")
			}
			return h.read(p)
		}})
	var id string
	for m := 0; m <= 30; m++ {
		h.now = minutes(m)
		h.ticks += 100 * 60 * 90 / 100
		failing = m == 22 || m == 23
		svc.TickSustained(context.Background(), h.now)
		if m == 21 {
			a := firingKeys(t, svc)
			if len(a) != 1 {
				t.Fatalf("expected firing before the failure: %+v", a)
			}
			id = a[0].ID
		}
	}
	a := firingKeys(t, svc)
	if len(a) != 1 || a[0].ID != id {
		t.Fatalf("alert lost or refired across stat failure: %+v (id %s)", a, id)
	}
	if resolved, _ := svc.Alerts(context.Background(), "resolved", 10); len(resolved) != 0 {
		t.Fatalf("alert resolved during transient failure: %+v", resolved)
	}
	// The delta after the failure spans both skipped ticks and stays near 90%.
	if !strings.Contains(a[0].Detail, "averaged 90% CPU") {
		t.Fatalf("detail: %s", a[0].Detail)
	}
}

func TestReadLinuxCPUHugeCounterDoesNotOverflow(t *testing.T) {
	const ticks = int64(2e10) // ~6.3 CPU-years; overflowed when scaled by 1e9 first
	read := func(string) ([]byte, error) {
		return []byte(fmt.Sprintf("412 (d) S 1 0 0 0 0 0 0 0 0 0 %d %d", ticks/2, ticks/2)), nil
	}
	got, err := readLinuxCPU(read, 412)
	if err != nil || got != time.Duration(ticks)*10*time.Millisecond || got <= 0 {
		t.Fatalf("cpu = %v, %v", got, err)
	}
	h := &linuxHost{start: svcT0.Add(-24 * time.Hour), ticks: ticks}
	st := newSustainedState(DefaultSustainedConfig(), nil, h.read, hostos.Linux)
	procs := []ProcObs{{PID: 412, Name: "d", Started: h.start}}
	h.now = svcT0
	st.cpuObservations(procs, h.now)
	h.now = svcT0.Add(10 * time.Second)
	h.ticks += 500
	obs := st.cpuObservations(procs, h.now)
	if len(obs) != 1 || obs[0].Skip || obs[0].CPUPct < 49.9 || obs[0].CPUPct > 50.1 {
		t.Fatalf("huge counter reset or mis-scaled the delta: %+v", obs)
	}
}
