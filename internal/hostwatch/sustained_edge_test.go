package hostwatch

import (
	"testing"
	"time"
)

// A long run of unreadable ticks must not let the old anchor sample stand in
// for window coverage once readings resume.
func TestSustainedSkippedRunDoesNotFakeCoverage(t *testing.T) {
	base := 15 * time.Second
	window := 16 * base
	started := atD(-5 * time.Hour)
	tr := NewSustainedTracker(tickCfg(window, base))
	tr.Observe(atD(0), []ProcObs{obs(10, "d", 99, started)})
	for d := base; d < 15*base; d += base {
		tr.Observe(atD(d), []ProcObs{skipObs(started)})
	}
	resume := 15 * base
	var firedAt time.Duration
	for d := resume; d <= resume+window+2*base; d += base {
		if got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)}); len(got) > 0 {
			firedAt = d
			break
		}
	}
	if firedAt == 0 {
		t.Fatal("never fired once a full window of fresh readings existed")
	}
	if firedAt-resume < window {
		t.Fatalf("fired %v after readings resumed, want at least a full %v window", firedAt-resume, window)
	}
}

// An alert that was already firing keeps its since-time across a skipped run.
func TestSustainedSkippedRunKeepsFiringAlert(t *testing.T) {
	base := 15 * time.Second
	tr, started, d := fireAtMinWindow(t)
	var since time.Time
	for _, f := range tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)}) {
		since = f.Since
	}
	for i := 1; i <= 12; i++ {
		d += base
		if got := tr.Observe(atD(d), []ProcObs{skipObs(started)}); len(got) != 0 && !got[0].Since.Equal(since) {
			t.Fatalf("since changed during skips: %+v", got)
		}
	}
	d += base
	got := tr.Observe(atD(d), []ProcObs{obs(10, "d", 99, started)})
	if len(got) != 1 || !got[0].Since.Equal(since) {
		t.Fatalf("firing alert lost across skipped run: %+v", got)
	}
}
