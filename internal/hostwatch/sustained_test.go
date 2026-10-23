package hostwatch

import (
	"strings"
	"testing"
	"time"
)

var sustainedT0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// step describes one tick: minutes since T0 and the CPU of the single
// process under test (negative means the process is absent).
type step struct {
	min float64
	cpu float64
}

func at(min float64) time.Time {
	return sustainedT0.Add(time.Duration(min * float64(time.Minute)))
}

func obs(pid int, name string, cpu float64, started time.Time) ProcObs {
	return ProcObs{PID: pid, PPID: 1, Name: name, CPUPct: cpu, Started: started}
}

func runSteps(tr *SustainedTracker, name string, started time.Time, steps []step) (last []SustainedFinding, everFired bool) {
	for _, s := range steps {
		var procs []ProcObs
		if s.cpu >= 0 {
			procs = append(procs, obs(10, name, s.cpu, started))
		}
		last = tr.Observe(at(s.min), procs)
		if len(last) > 0 {
			everFired = true
		}
	}
	return last, everFired
}

func ramp(from, to, every float64, cpu float64) []step {
	var out []step
	for m := from; m <= to+1e-9; m += every {
		out = append(out, step{m, cpu})
	}
	return out
}

func TestSustainedTrackerTable(t *testing.T) {
	started := at(-300)
	cfg := DefaultSustainedConfig()
	cfg.Allowlist = []string{"backupd*"}
	tests := []struct {
		name      string
		proc      string
		steps     []step
		wantFired bool
		wantLast  bool
	}{
		{"steady 90% for 20m fires", "diagnosticd", ramp(0, 20, 0.25, 90), true, true},
		{"just under window does not fire", "diagnosticd", ramp(0, 12, 0.25, 90), false, false},
		{"below threshold never fires", "diagnosticd", ramp(0, 60, 0.25, 20), false, false},
		{"allowlisted never fires", "backupd-helper", ramp(0, 60, 0.25, 99), false, false},
		{"allowlist matches path basename", "/usr/libexec/Backupd", ramp(0, 60, 0.25, 99), false, false},
		{"bursty 2m on / 2m off never fires", "diagnosticd", burst(60, 2, 2), false, false},
		{"single dip inside window tolerated", "diagnosticd", append(append(ramp(0, 10, 0.25, 90), step{10.25, 5}), ramp(10.5, 20, 0.25, 90)...), true, true},
		{"fires then clears after sustained drop", "diagnosticd", append(ramp(0, 20, 0.25, 90), ramp(20.25, 40, 0.25, 1)...), true, false},
		{"process exit clears", "diagnosticd", append(ramp(0, 20, 0.25, 90), step{20.25, -1}), true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewSustainedTracker(cfg)
			last, fired := runSteps(tr, tc.proc, started, tc.steps)
			if fired != tc.wantFired || (len(last) > 0) != tc.wantLast {
				t.Fatalf("fired=%v last=%+v, want fired=%v last=%v", fired, last, tc.wantFired, tc.wantLast)
			}
		})
	}
}

func burst(total, on, off int) []step {
	var out []step
	for q := 0; q <= total*4; q++ {
		m := float64(q) / 4
		cpu := 5.0
		if int(m)%(on+off) < on {
			cpu = 95
		}
		out = append(out, step{m, cpu})
	}
	return out
}

func TestSustainedDurationExceedsWindowAndSince(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	started := at(-60)
	last, _ := runSteps(tr, "OSLogService", started, ramp(0, 180, 0.5, 90))
	if len(last) != 1 {
		t.Fatalf("findings: %+v", last)
	}
	f := last[0]
	if f.Duration < 160*time.Minute || f.Since.After(at(16)) || f.AvgCPU < 89 || f.Fraction != 1 {
		t.Fatalf("finding %+v", f)
	}
}

func TestSustainedPIDReuseKeyedByStartTime(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	oldStart := at(-300)
	if _, fired := runSteps(tr, "diagnosticd", oldStart, ramp(0, 20, 0.25, 90)); !fired {
		t.Fatal("expected first process to fire")
	}
	// Same PID, new process started 1 minute ago: history must reset.
	newStart := at(20)
	got := tr.Observe(at(20.25), []ProcObs{obs(10, "mdworker", 90, newStart)})
	if len(got) != 0 {
		t.Fatalf("reused PID inherited history: %+v", got)
	}
	// Jitter within tolerance keeps the same identity.
	tr = NewSustainedTracker(DefaultSustainedConfig())
	var last []SustainedFinding
	for m := 0.0; m <= 20; m += 0.25 {
		jitter := time.Duration(int(m*4)%2) * time.Second
		last = tr.Observe(at(m), []ProcObs{obs(10, "diagnosticd", 90, oldStart.Add(jitter))})
	}
	if len(last) != 1 {
		t.Fatalf("start-time jitter broke tracking: %+v", last)
	}
}

func TestSustainedGapResetsTracking(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	started := at(-300)
	runSteps(tr, "diagnosticd", started, ramp(0, 10, 0.25, 90))
	// Host slept for an hour; the next samples must not count as continuous.
	got := tr.Observe(at(70), []ProcObs{obs(10, "diagnosticd", 90, started)})
	if len(got) != 0 {
		t.Fatalf("gap counted as sustained: %+v", got)
	}
}

func TestSustainedWindowEviction(t *testing.T) {
	tr := NewSustainedTracker(DefaultSustainedConfig())
	started := at(-300)
	runSteps(tr, "diagnosticd", started, ramp(0, 20, 0.25, 90))
	for _, e := range tr.entries {
		if len(e.points) > 15*4+2 {
			t.Fatalf("window not evicted: %d points", len(e.points))
		}
	}
	runSteps(tr, "diagnosticd", started, ramp(20.25, 40, 0.25, 1))
	if len(tr.entries) != 0 {
		t.Fatalf("idle process still tracked: %+v", tr.entries)
	}
}

func TestParentChain(t *testing.T) {
	table := map[int]ProcObs{
		1:   {PID: 1, PPID: 0, Name: "/sbin/launchd"},
		50:  {PID: 50, PPID: 1, Name: "/usr/bin/Terminal"},
		60:  {PID: 60, PPID: 50, Name: "zsh"},
		70:  {PID: 70, PPID: 60, Name: "log"},
		900: {PID: 900, PPID: 901, Name: "loopA"},
		901: {PID: 901, PPID: 900, Name: "loopB"},
	}
	if got := formatChain(ParentChain(table, 70, 8)); got != "launchd(1) > Terminal(50) > zsh(60) > log(70)" {
		t.Fatalf("chain: %s", got)
	}
	if got := formatChain(ParentChain(table, 70, 1)); got != "zsh(60) > log(70)" {
		t.Fatalf("depth limit: %s", got)
	}
	if got := ParentChain(table, 900, 8); len(got) != 2 {
		t.Fatalf("cycle not cut: %+v", got)
	}
	if got := ParentChain(table, 12345, 8); len(got) != 0 {
		t.Fatalf("unknown pid: %+v", got)
	}
}

func TestSustainedDetailHints(t *testing.T) {
	f := SustainedFinding{PID: 412, Name: "diagnosticd", Started: at(-300), Since: at(0), Duration: 3 * time.Hour, AvgCPU: 87}
	chain := []ChainLink{{1, "launchd"}, {412, "diagnosticd"}}
	for _, tc := range []struct {
		name, goos, sample string
		want, absent       []string
	}{
		{"diagnosticd", "darwin", "top: __ulock_wait", []string{"unified log", "`log stream`", "OSLogStore", "launchd(1) > diagnosticd(412)", "for 3h0m0s", "up 5h0m0s", "next: sample 412 10", "sample: top: __ulock_wait", "87% CPU"}, nil},
		{"OSLogService", "darwin", "", []string{"unified log"}, []string{"sample:"}},
		{"/usr/libexec/mds_stores", "darwin", "", []string{"Spotlight"}, []string{"unified log"}},
		{"java", "linux", "", []string{"next: top -H -p 412"}, []string{"hint:"}},
	} {
		f.Name = tc.name
		got := SustainedDetail(f, at(0), chain, tc.goos, tc.sample)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s detail lacks %q: %s", tc.name, w, got)
			}
		}
		for _, w := range tc.absent {
			if strings.Contains(got, w) {
				t.Errorf("%s detail has unexpected %q: %s", tc.name, w, got)
			}
		}
	}
}
