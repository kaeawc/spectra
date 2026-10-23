package hostwatch

import (
	"testing"
	"time"
)

func tickCfg(window, base time.Duration) SustainedConfig {
	cfg := DefaultSustainedConfig()
	cfg.Window, cfg.Interval, cfg.MaxGap = window, base, 12*base
	return cfg
}

func atD(d time.Duration) time.Time { return sustainedT0.Add(d) }

func skipObs(started time.Time) ProcObs {
	p := obs(10, "d", 0, started)
	p.Skip = true
	return p
}

func TestSustainedFiringNeedsRealCoverageAcrossIntervals(t *testing.T) {
	started := atD(-5 * time.Hour)
	for _, tc := range []struct {
		name         string
		window, base time.Duration
	}{
		{"window == 4*base", time.Minute, 15 * time.Second},
		{"window == 8*base", 2 * time.Minute, 15 * time.Second},
		{"default 15s / 15m", 15 * time.Minute, 15 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewSustainedTracker(tickCfg(tc.window, tc.base))
			if got := tr.Observe(atD(0), []ProcObs{obs(10, "d", 99, started)}); len(got) != 0 {
				t.Fatalf("single sample fired: %+v", got)
			}
			tr = NewSustainedTracker(tickCfg(tc.window, tc.base))
			var fired time.Duration
			for d := time.Duration(0); d <= tc.window+2*tc.base; d += tc.base {
				got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)})
				if len(got) > 0 && fired == 0 {
					fired = d
				}
			}
			if fired == 0 || fired < tc.window-tc.base {
				t.Fatalf("fired at %v, want coverage of the %v window", fired, tc.window)
			}
		})
	}
	// Two readings at the window's edges with nothing between must not fire.
	tr := NewSustainedTracker(tickCfg(time.Minute, 15*time.Second))
	tr.Observe(atD(0), []ProcObs{obs(10, "d", 99, started)})
	for d := 15 * time.Second; d < time.Minute; d += 15 * time.Second {
		tr.Observe(atD(d), []ProcObs{skipObs(started)})
	}
	if got := tr.Observe(atD(time.Minute), []ProcObs{obs(10, "d", 99, started)}); len(got) != 0 {
		t.Fatalf("two edge samples fired: %+v", got)
	}
}

func fireAtWindow4(t *testing.T) (tr *SustainedTracker, started time.Time, next time.Duration) {
	t.Helper()
	base := 15 * time.Second
	tr = NewSustainedTracker(tickCfg(4*base, base))
	started = atD(-5 * time.Hour)
	var d time.Duration
	var got []SustainedFinding
	for ; d <= 10*base; d += base {
		got = tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)})
	}
	if len(got) != 1 {
		t.Fatalf("expected firing: %+v", got)
	}
	return tr, started, d
}

func TestSustainedSingleLowSampleDoesNotClear(t *testing.T) {
	base := 15 * time.Second
	tr, started, d := fireAtWindow4(t)
	if got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 1, started)}); len(got) != 1 {
		t.Fatalf("one low sample cleared the alert: %+v", got)
	}
	var got []SustainedFinding
	for i := 1; i <= 8; i++ {
		got = tr.Observe(atD(d+time.Duration(i)*base), []ProcObs{obs(10, "d", 1, started)})
	}
	if len(got) != 0 {
		t.Fatalf("sustained idle did not clear: %+v", got)
	}
	// Carried entry after a gap: one low sample keeps it, a second drops it.
	tr, started, d = fireAtWindow4(t)
	d += time.Hour
	if got = tr.Observe(atD(d), []ProcObs{obs(10, "d", 1, started)}); len(got) != 1 {
		t.Fatalf("one low sample dropped the carried entry: %+v", got)
	}
	for i := 1; i <= 3; i++ {
		got = tr.Observe(atD(d+time.Duration(i)*base), []ProcObs{obs(10, "d", 1, started)})
	}
	if len(got) != 0 {
		t.Fatalf("fresh idle samples did not drop the carried entry: %+v", got)
	}
}

func TestSustainedPermanentSkipAgesOut(t *testing.T) {
	base := 15 * time.Second
	tr, started, d := fireAtWindow4(t)
	lastGood := d - base
	var got []SustainedFinding
	for ; d < lastGood+time.Minute; d += base {
		if got = tr.Observe(atD(d), []ProcObs{skipObs(started)}); len(got) != 1 {
			t.Fatalf("aged out early at %v: %+v", d-lastGood, got)
		}
	}
	if got = tr.Observe(atD(d), []ProcObs{skipObs(started)}); len(got) != 0 {
		t.Fatalf("permanently unreadable process still firing: %+v", got)
	}
}

func TestSustainedPIDReuseDuringSkipResets(t *testing.T) {
	tr, _, d := fireAtWindow4(t)
	reused := skipObs(atD(d).Add(-time.Second))
	reused.Name = "other"
	if got := tr.Observe(atD(d), []ProcObs{reused}); len(got) != 0 {
		t.Fatalf("reused PID inherited the old alert: %+v", got)
	}
}
