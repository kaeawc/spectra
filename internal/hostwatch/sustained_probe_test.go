package hostwatch

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/proc"
)

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:07":      7 * time.Second,
		"12:34":      12*time.Minute + 34*time.Second,
		"01:02:03":   time.Hour + 2*time.Minute + 3*time.Second,
		"2-03:04:05": 51*time.Hour + 4*time.Minute + 5*time.Second,
	} {
		got, err := parseEtime(in)
		if err != nil || got != want {
			t.Errorf("parseEtime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "5", "a:b", "x-01:02", "1:2:3:4", "-1:00"} {
		if _, err := parseEtime(in); err == nil {
			t.Errorf("parseEtime(%q) accepted", in)
		}
	}
}

func TestParseProcObs(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	out := []byte("  1     0   0.0 5-00:00:00 /sbin/launchd\n412 1 87,5 05:00:10 /usr/libexec/diagnosticd\n7 1 1.0 00:05 Some App Helper\nbad line\n")
	got := parseProcObs(out, now)
	if len(got) != 3 || got[1].CPUPct != 87.5 || got[1].Name != "/usr/libexec/diagnosticd" || !got[1].Started.Equal(now.Add(-5*time.Hour-10*time.Second)) || got[2].Name != "Some App Helper" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestSampleSummaries(t *testing.T) {
	darwin := "Call graph:\n    1000 Thread_1\n\nTotal number in stack\n\nSort by top of stack, same collapsing path:\n        __ulock_wait  (in libsystem_kernel.dylib)        1000\n        mach_msg2_trap  (in libsystem_kernel.dylib)  500\n\nBinary Images:\n"
	if got := summarizeDarwinSample(darwin); !strings.Contains(got, "__ulock_wait (in libsystem_kernel.dylib) 1000 | mach_msg2_trap") || strings.Contains(got, "Binary") {
		t.Fatalf("darwin summary: %q", got)
	}
	if got := summarizeDarwinSample("nothing useful"); got != "" {
		t.Fatalf("darwin no section: %q", got)
	}
	linux := "11 2.0 worker\n12 80.5 hot thread\n13 9.0 io\n14 1.0 idle\n"
	if got := summarizeLinuxThreads(linux); got != "busiest threads: tid 12 hot thread 80% | tid 13 io 9% | tid 11 worker 2%" {
		t.Fatalf("linux summary: %q", got)
	}
}

func TestEngineSustainedScopeIsolation(t *testing.T) {
	e := NewEngine(idgen.NewSequence("s"))
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cond := Condition{sustainedKeyPrefix + "diagnosticd:412", "warn", "t", "d"}
	if ev := e.EvaluateSustained(now, []Condition{cond}); len(ev) != 1 || ev[0].Type != "fired" {
		t.Fatalf("fire: %+v", ev)
	}
	// Slow and spawn evaluations with no conditions must not resolve it.
	for i := 0; i < 5; i++ {
		if ev := e.EvaluateSlow(now, nil); len(ev) != 0 {
			t.Fatalf("slow scope resolved sustained alert: %+v", ev)
		}
		if ev := e.EvaluateSpawn(now, nil); len(ev) != 0 {
			t.Fatalf("spawn scope resolved sustained alert: %+v", ev)
		}
	}
	if ev := e.EvaluateSustained(now, nil); len(ev) != 0 {
		t.Fatalf("resolved too early: %+v", ev)
	}
	if ev := e.EvaluateSustained(now, nil); len(ev) != 1 || ev[0].Type != "resolved" {
		t.Fatalf("resolve: %+v", ev)
	}
}

// psRunner serves scripted ps/sample output for the probe.
type psRunner struct {
	now   time.Time
	start time.Time
	cpu   float64
	calls []string
}

func (r *psRunner) Run(_ context.Context, cmd proc.Cmd) (proc.Result, error) {
	r.calls = append(r.calls, cmd.Name+" "+strings.Join(cmd.Args, " "))
	switch cmd.Name {
	case "ps":
		age := int(r.now.Sub(r.start).Seconds())
		etime := fmt.Sprintf("%02d:%02d:%02d", age/3600, age/60%60, age%60)
		return proc.Result{Stdout: []byte(fmt.Sprintf("1 0 0.0 9-00:00:00 /sbin/launchd\n412 1 %.1f %s /usr/libexec/diagnosticd\n600 1 99.0 01:00:00 /usr/bin/backupd\n", r.cpu, etime))}, nil
	case "sample":
		return proc.Result{Stdout: []byte("Sort by top of stack, same collapsing path:\n  __ulock_wait 1000\n\n")}, nil
	}
	return proc.Result{ExitCode: -1}, fmt.Errorf("unexpected %s", cmd.Name)
}

func TestServiceSustainedAlertEndToEnd(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sustained.Allowlist = []string{"backupd"}
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	r := &psRunner{cpu: 90, start: t0.Add(-5 * time.Hour)}
	svc := NewService(ServiceOptions{Store: testDB(t), Config: cfg, Runner: r, OS: hostos.Darwin, IDs: idgen.NewSequence("a"), Collector: &sequenceCollector{}})
	ctx := context.Background()
	tick := func(m int) {
		r.now = t0.Add(time.Duration(m) * time.Minute)
		svc.TickSustained(ctx, r.now)
	}
	for m := 0; m <= 20; m++ {
		tick(m)
	}
	alerts, err := svc.Alerts(ctx, "firing", 10)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("alerts %+v err %v", alerts, err)
	}
	a := alerts[0]
	for _, want := range []string{"diagnosticd (pid 412)", "owner chain: launchd(1) > diagnosticd(412)", "log stream", "sample top of stack: __ulock_wait 1000", "next: sample 412 10", "up 5h"} {
		if !strings.Contains(a.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, a.Detail)
		}
	}
	if a.Key != "cpu.sustained:diagnosticd:412" || strings.Contains(a.Detail, "backupd") {
		t.Fatalf("key/allowlist: %+v", a)
	}
	samples := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, "sample ") {
			samples++
		}
	}
	if samples != 1 {
		t.Fatalf("sample should run once per alert, ran %d", samples)
	}
	// Load drops: alert resolves after the window drains and two clear ticks.
	r.cpu = 1
	for m := 21; m <= 40; m++ {
		tick(m)
	}
	if firing, _ := svc.Alerts(ctx, "firing", 10); len(firing) != 0 {
		t.Fatalf("alert did not resolve: %+v", firing)
	}
	if resolved, _ := svc.Alerts(ctx, "resolved", 10); len(resolved) != 1 {
		t.Fatalf("resolved alerts: %+v", resolved)
	}
}
