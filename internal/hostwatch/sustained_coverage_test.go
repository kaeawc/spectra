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

// periods lists realistic tick periods for a base interval: exact, base plus
// work, 1% slow, and the same two under 4x self-guard backoff.
func periods(base time.Duration) map[string]time.Duration {
	return map[string]time.Duration{
		"base":            base,
		"base+10ms":       base + 10*time.Millisecond,
		"base*1.01":       base + base/100,
		"4x backoff":      4 * base,
		"4x backoff+10ms": 4*base + 10*time.Millisecond,
		"4x backoff*1.01": 4*base + 4*base/100,
	}
}

func TestSustainedFiresAfterWindowUnderJitter(t *testing.T) {
	started := atD(-5 * time.Hour)
	base := 15 * time.Second
	for _, w := range []struct {
		name   string
		window time.Duration
	}{{"default 15m", 15 * time.Minute}, {"minimum 16*base", 16 * base}} {
		for pname, period := range periods(base) {
			t.Run(w.name+"/"+pname, func(t *testing.T) {
				tr := NewSustainedTracker(tickCfg(w.window, base))
				var fired time.Duration
				for d := time.Duration(0); d <= w.window+3*period; d += period {
					got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)})
					if len(got) > 0 && fired == 0 {
						fired = d
					}
					if d < w.window && len(got) > 0 {
						t.Fatalf("fired after only %v of a %v window", d, w.window)
					}
				}
				if fired == 0 || fired > w.window+period {
					t.Fatalf("fired at %v, want within one tick of the %v window", fired, w.window)
				}
			})
		}
	}
}

func TestSustainedNeverFiresOnShortOrSingleSample(t *testing.T) {
	started := atD(-5 * time.Hour)
	base := 15 * time.Second
	for pname, period := range periods(base) {
		t.Run(pname, func(t *testing.T) {
			tr := NewSustainedTracker(tickCfg(16*base, base))
			if got := tr.Observe(atD(0), []ProcObs{obs(10, "d", 99, started)}); len(got) != 0 {
				t.Fatalf("single sample fired: %+v", got)
			}
			// Load that stops just short of the window must not fire.
			tr = NewSustainedTracker(tickCfg(16*base, base))
			for d := time.Duration(0); d+period < 16*base; d += period {
				if got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)}); len(got) != 0 {
					t.Fatalf("fired at %v, short of the window", d)
				}
			}
		})
	}
	// Two readings at the window's edges with nothing between must not fire.
	tr := NewSustainedTracker(tickCfg(16*base, base))
	tr.Observe(atD(0), []ProcObs{obs(10, "d", 99, started)})
	for d := base; d < 16*base; d += base {
		tr.Observe(atD(d), []ProcObs{skipObs(started)})
	}
	if got := tr.Observe(atD(16*base), []ProcObs{obs(10, "d", 99, started)}); len(got) != 0 {
		t.Fatalf("two edge samples fired: %+v", got)
	}
}

func TestSustainedMinAbovePointsReachable(t *testing.T) {
	base := 15 * time.Second
	for _, w := range []time.Duration{16 * base, 32 * base, 15 * time.Minute} {
		tr := NewSustainedTracker(tickCfg(w, base))
		// Worst realistic effective period: 4x backoff, 1% slow.
		worst := 4*base + 4*base/100
		held := int(w / worst)
		if need := tr.minAbovePoints(); need > held {
			t.Errorf("window %v: needs %d above samples but only %d fit at %v", w, need, held, worst)
		}
	}
}

func fireAtMinWindow(t *testing.T) (tr *SustainedTracker, started time.Time, next time.Duration) {
	t.Helper()
	base := 15 * time.Second
	period := base + 10*time.Millisecond
	tr = NewSustainedTracker(tickCfg(16*base, base))
	started = atD(-5 * time.Hour)
	var got []SustainedFinding
	d := time.Duration(0)
	for ; d <= 17*base; d += period {
		got = tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)})
	}
	if len(got) != 1 {
		t.Fatalf("expected firing: %+v", got)
	}
	return tr, started, d
}

func TestSustainedSingleLowSampleDoesNotClear(t *testing.T) {
	period := 15*time.Second + 10*time.Millisecond
	tr, started, d := fireAtMinWindow(t)
	if got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 1, started)}); len(got) != 1 {
		t.Fatalf("one low sample cleared the alert: %+v", got)
	}
	var got []SustainedFinding
	for i := 1; i <= 20; i++ {
		got = tr.Observe(atD(d+time.Duration(i)*period), []ProcObs{obs(10, "d", 1, started)})
	}
	if len(got) != 0 {
		t.Fatalf("sustained idle did not clear: %+v", got)
	}
	// Carried entry after a gap: one low sample keeps it, fresh ones drop it.
	tr, started, d = fireAtMinWindow(t)
	d += time.Hour
	if got = tr.Observe(atD(d), []ProcObs{obs(10, "d", 1, started)}); len(got) != 1 {
		t.Fatalf("one low sample dropped the carried entry: %+v", got)
	}
	for i := 1; i <= 3; i++ {
		got = tr.Observe(atD(d+time.Duration(i)*period), []ProcObs{obs(10, "d", 1, started)})
	}
	if len(got) != 0 {
		t.Fatalf("fresh idle samples did not drop the carried entry: %+v", got)
	}
}

func TestSustainedPermanentSkipAgesOut(t *testing.T) {
	base := 15 * time.Second
	period := base + 10*time.Millisecond
	tr, started, d := fireAtMinWindow(t)
	lastGood := d - period
	var got []SustainedFinding
	for ; d < lastGood+16*base; d += period {
		if got = tr.Observe(atD(d), []ProcObs{skipObs(started)}); len(got) != 1 {
			t.Fatalf("aged out early at %v: %+v", d-lastGood, got)
		}
	}
	if got = tr.Observe(atD(d), []ProcObs{skipObs(started)}); len(got) != 0 {
		t.Fatalf("permanently unreadable process still firing: %+v", got)
	}
}

func TestSustainedPIDReuseDuringSkipResets(t *testing.T) {
	tr, _, d := fireAtMinWindow(t)
	reused := skipObs(atD(d).Add(-time.Second))
	reused.Name = "other"
	if got := tr.Observe(atD(d), []ProcObs{reused}); len(got) != 0 {
		t.Fatalf("reused PID inherited the old alert: %+v", got)
	}
}
