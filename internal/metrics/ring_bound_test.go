package metrics

import (
	"testing"
	"time"
)

// A long-lived process sampled every 15 s keeps 30 minutes of samples, not
// maxSamples (which would be 7.5 h at that cadence), and the backing array
// stays proportional to what is held.
func TestRingBufferBoundedByRetainWindow(t *testing.T) {
	rb := &RingBuffer{}
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	const step = 15 * time.Second
	for i := 0; i < 4*60*4; i++ { // four hours
		rb.Add(Sample{TakenAt: base.Add(time.Duration(i) * step), PID: 1, RSSKiB: int64(i)})
	}
	want := int(DefaultRetainWindow/step) + 1
	if rb.Len() != want {
		t.Fatalf("Len = %d, want %d", rb.Len(), want)
	}
	if c := len(rb.buf); c > 2*want {
		t.Fatalf("backing capacity %d for %d samples", c, want)
	}
	got := rb.Samples()
	if span := got[len(got)-1].TakenAt.Sub(got[0].TakenAt); span != DefaultRetainWindow {
		t.Fatalf("span = %s, want %s", span, DefaultRetainWindow)
	}
}

// Churning PIDs (each seen for a few ticks, then gone) stay bounded: a PID is
// dropped once its newest sample is older than the retain window, so the map
// holds at most the PIDs seen in the last window.
func TestCollectorChurningPIDsStayBounded(t *testing.T) {
	c := NewCollector()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	const step = 15 * time.Second
	const perTick = 50 // new short-lived pids per tick
	ticksPerWindow := int(DefaultRetainWindow / step)
	maxLen := 0
	for tick := 0; tick < 6*ticksPerWindow; tick++ {
		at := base.Add(time.Duration(tick) * step)
		c.Add(Sample{TakenAt: at, PID: 1}) // a long-lived process
		for j := 0; j < perTick; j++ {
			c.Add(Sample{TakenAt: at, PID: 1000 + tick*perTick + j})
		}
		if tick%4 == 0 { // flush once a minute, as the watch does
			_, commit := c.FlushAggregates(at, DefaultRetainWindow)
			commit()
		}
		maxLen = max(maxLen, c.Len())
	}
	// One window of churn plus a minute of flush lag, plus the long-lived pid.
	bound := (ticksPerWindow+5)*perTick + 1
	if maxLen > bound {
		t.Fatalf("collector held %d pids, bound %d", maxLen, bound)
	}
	if got := len(c.Recent(1, 0)); got > ticksPerWindow+1 {
		t.Fatalf("long-lived pid kept %d samples", got)
	}
}

// Each completed minute is flushed once; the newest (possibly partial) minute
// is flushed again so its final values replace the partial row.
func TestFlushAggregatesIncremental(t *testing.T) {
	c := NewCollector()
	m0 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	add := func(at time.Time, rss int64) { c.Add(Sample{TakenAt: at, PID: 7, RSSKiB: rss}) }
	add(m0.Add(10*time.Second), 100)
	add(m0.Add(40*time.Second), 300)
	add(m0.Add(70*time.Second), 500) // minute 1, partial
	flush := func(at time.Time) []Aggregate {
		aggs, commit := c.FlushAggregates(at, DefaultRetainWindow)
		commit()
		return aggs
	}
	first := flush(m0.Add(75 * time.Second))
	if len(first) != 2 {
		t.Fatalf("first flush = %d aggregates, want 2", len(first))
	}
	add(m0.Add(100*time.Second), 700) // completes minute 1
	add(m0.Add(130*time.Second), 900) // minute 2
	second := flush(m0.Add(135 * time.Second))
	if len(second) != 2 {
		t.Fatalf("second flush = %+v, want minutes 1 and 2 only", second)
	}
	for _, a := range second {
		switch {
		case a.MinuteAt.Equal(m0.Add(time.Minute)):
			if a.SampleCount != 2 || a.AvgRSSKiB != 600 {
				t.Fatalf("minute 1 = %+v, want 2 samples avg 600", a)
			}
		case a.MinuteAt.Equal(m0.Add(2 * time.Minute)):
		default:
			t.Fatalf("unexpected minute re-flushed: %+v", a)
		}
	}
	if third := flush(m0.Add(140 * time.Second)); len(third) != 1 {
		t.Fatalf("third flush = %+v, want only the newest minute", third)
	}
	// Exited for longer than the window: flushed out of RAM.
	flush(m0.Add(130*time.Second + DefaultRetainWindow + time.Second))
	if c.Len() != 0 {
		t.Fatalf("stale pid kept: %d", c.Len())
	}
}

func TestCollectorRecentLimit(t *testing.T) {
	c := NewCollector()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		c.Add(Sample{TakenAt: base.Add(time.Duration(i) * time.Second), PID: 3, RSSKiB: int64(i)})
	}
	got := c.Recent(3, 3)
	if len(got) != 3 || got[0].RSSKiB != 7 || got[2].RSSKiB != 9 {
		t.Fatalf("Recent(3) = %+v", got)
	}
	got[0].RSSKiB = -1 // callers own the copy
	if c.Recent(3, 3)[0].RSSKiB != 7 {
		t.Fatal("Recent returned the internal slice")
	}
}

// The buffer grows for a dense cadence, shrinks when the cadence slows, and
// keeps chronological order across wraps and resizes.
func TestRingBufferResizeKeepsOrder(t *testing.T) {
	rb := &RingBuffer{}
	at := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2*maxSamples; i++ {
		at = at.Add(time.Second)
		rb.Add(Sample{TakenAt: at, PID: 1})
	}
	if rb.Len() != maxSamples {
		t.Fatalf("Len = %d, want %d", rb.Len(), maxSamples)
	}
	for i := 0; i < 120; i++ {
		at = at.Add(time.Minute)
		rb.Add(Sample{TakenAt: at, PID: 1})
	}
	if rb.Len() != 31 || len(rb.buf) > 4*rb.Len() {
		t.Fatalf("Len = %d, buffer %d after the cadence slowed", rb.Len(), len(rb.buf))
	}
	got := rb.Samples()
	for i := 1; i < len(got); i++ {
		if !got[i].TakenAt.After(got[i-1].TakenAt) {
			t.Fatalf("samples out of order at %d: %v then %v", i, got[i-1].TakenAt, got[i].TakenAt)
		}
	}
	if !got[len(got)-1].TakenAt.Equal(at) {
		t.Fatalf("newest = %v, want %v", got[len(got)-1].TakenAt, at)
	}
}

// A sample after a gap longer than the window (sleep, suspend) empties a
// large buffer; it must shrink to a usable size, not to zero.
func TestRingBufferGapAfterDenseCadence(t *testing.T) {
	rb := &RingBuffer{}
	at := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 600; i++ {
		at = at.Add(time.Second)
		rb.Add(Sample{TakenAt: at, PID: 1})
	}
	at = at.Add(2 * DefaultRetainWindow)
	rb.Add(Sample{TakenAt: at, PID: 1, RSSKiB: 9})
	rb.Add(Sample{TakenAt: at.Add(time.Second), PID: 1, RSSKiB: 10})
	got := rb.Samples()
	if len(got) != 2 || got[0].RSSKiB != 9 || got[1].RSSKiB != 10 || len(rb.buf) > 16 {
		t.Fatalf("after gap: %+v, buffer %d", got, len(rb.buf))
	}
}

// Without a commit (the save failed) the next flush returns the same minutes
// and the stale PID stays until a flush commits.
func TestFlushAggregatesUncommittedRetries(t *testing.T) {
	c := NewCollector()
	m0 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	c.Add(Sample{TakenAt: m0.Add(10 * time.Second), PID: 7, RSSKiB: 100})
	c.Add(Sample{TakenAt: m0.Add(70 * time.Second), PID: 7, RSSKiB: 200})
	late := m0.Add(DefaultRetainWindow + 2*time.Minute)
	failed, _ := c.FlushAggregates(late, DefaultRetainWindow) // save failed: no commit
	if len(failed) != 2 || c.Len() != 1 {
		t.Fatalf("failed flush = %d aggregates, %d pids", len(failed), c.Len())
	}
	retried, commit := c.FlushAggregates(late, DefaultRetainWindow)
	if len(retried) != 2 {
		t.Fatalf("retry = %+v, want both minutes again", retried)
	}
	commit()
	if c.Len() != 0 {
		t.Fatalf("stale pid kept after commit: %d", c.Len())
	}
}
