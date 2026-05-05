package metrics

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- RingBuffer ---

func TestRingBufferOrdering(t *testing.T) {
	rb := &RingBuffer{}
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		rb.Add(Sample{TakenAt: base.Add(time.Duration(i) * time.Second), PID: 1, RSSKiB: int64(i * 100)})
	}
	got := rb.Samples()
	if len(got) != 5 {
		t.Fatalf("expected 5 samples, got %d", len(got))
	}
	// Should be oldest first.
	for i, s := range got {
		if s.RSSKiB != int64(i*100) {
			t.Errorf("sample[%d] RSSKiB = %d, want %d", i, s.RSSKiB, i*100)
		}
	}
}

func TestRingBufferWrap(t *testing.T) {
	rb := &RingBuffer{}
	// Fill more than maxSamples to test wrap.
	base := time.Now().UTC()
	for i := 0; i < maxSamples+10; i++ {
		rb.Add(Sample{TakenAt: base.Add(time.Duration(i) * time.Second), PID: 1, RSSKiB: int64(i)})
	}
	got := rb.Samples()
	if len(got) != maxSamples {
		t.Fatalf("expected %d samples after wrap, got %d", maxSamples, len(got))
	}
	// The oldest retained sample should be the 11th (index 10).
	if got[0].RSSKiB != 10 {
		t.Errorf("oldest RSSKiB = %d, want 10", got[0].RSSKiB)
	}
	// The newest should be the last added.
	if got[maxSamples-1].RSSKiB != int64(maxSamples+9) {
		t.Errorf("newest RSSKiB = %d, want %d", got[maxSamples-1].RSSKiB, maxSamples+9)
	}
}

func TestRingBufferAggregate(t *testing.T) {
	rb := &RingBuffer{}
	// 3 samples in minute 0, 2 in minute 1.
	m0 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	m1 := m0.Add(time.Minute)
	for _, s := range []Sample{
		{TakenAt: m0.Add(10 * time.Second), PID: 1, RSSKiB: 1000, CPUPct: 1.0},
		{TakenAt: m0.Add(20 * time.Second), PID: 1, RSSKiB: 2000, CPUPct: 2.0},
		{TakenAt: m0.Add(30 * time.Second), PID: 1, RSSKiB: 3000, CPUPct: 3.0},
		{TakenAt: m1.Add(10 * time.Second), PID: 1, RSSKiB: 4000, CPUPct: 4.0},
		{TakenAt: m1.Add(20 * time.Second), PID: 1, RSSKiB: 5000, CPUPct: 5.0},
	} {
		rb.Add(s)
	}
	aggs := rb.Aggregate()
	if len(aggs) != 2 {
		t.Fatalf("expected 2 aggregates (one per minute), got %d", len(aggs))
	}
	// Find minute 0 aggregate.
	var agg0 Aggregate
	for _, a := range aggs {
		if a.MinuteAt.Equal(m0) {
			agg0 = a
		}
	}
	if agg0.SampleCount != 3 {
		t.Errorf("minute0 sample_count = %d, want 3", agg0.SampleCount)
	}
	if agg0.AvgRSSKiB != 2000 {
		t.Errorf("minute0 avg_rss = %d, want 2000", agg0.AvgRSSKiB)
	}
	if agg0.MaxRSSKiB != 3000 {
		t.Errorf("minute0 max_rss = %d, want 3000", agg0.MaxRSSKiB)
	}
	if agg0.MaxCPUPct != 3.0 {
		t.Errorf("minute0 max_cpu = %v, want 3.0", agg0.MaxCPUPct)
	}
}

// --- Collector ---

func TestCollectorAddAndRecent(t *testing.T) {
	c := NewCollector()
	base := time.Now().UTC()
	for i := 0; i < 10; i++ {
		c.Add(Sample{TakenAt: base.Add(time.Duration(i) * time.Second), PID: 42, RSSKiB: int64(i * 100)})
	}
	recent := c.Recent(42, 5)
	if len(recent) != 5 {
		t.Fatalf("expected 5 recent samples, got %d", len(recent))
	}
	// Most recent 5 (indices 5-9, RSS 500-900).
	if recent[0].RSSKiB != 500 {
		t.Errorf("oldest of recent 5: RSSKiB = %d, want 500", recent[0].RSSKiB)
	}
}

func TestCollectorPIDs(t *testing.T) {
	c := NewCollector()
	c.Add(Sample{PID: 1, TakenAt: time.Now()})
	c.Add(Sample{PID: 2, TakenAt: time.Now()})
	c.Add(Sample{PID: 1, TakenAt: time.Now()})
	pids := c.PIDs()
	if len(pids) != 2 {
		t.Errorf("expected 2 PIDs, got %d: %v", len(pids), pids)
	}
}

func TestCollectorMissingPID(t *testing.T) {
	c := NewCollector()
	if got := c.Recent(999, 10); got != nil {
		t.Errorf("expected nil for unknown PID, got %v", got)
	}
}

// --- Sampler parser ---

func TestParseSampleLine(t *testing.T) {
	at := time.Now()
	cases := []struct {
		line   string
		wantOK bool
		pid    int
		rss    int64
		cpuPct float64
	}{
		{"  412  12345  67890  1.2", true, 412, 12345, 1.2},
		{"1 100 200 0.5", true, 1, 100, 200}, // vsz in field 3, cpu in 4 — wait, check order
		{"", false, 0, 0, 0},
		{"abc 100 200 1.0", false, 0, 0, 0},
	}
	for _, c := range cases {
		s, ok := parseSampleLine(c.line, at)
		if ok != c.wantOK {
			t.Errorf("parseSampleLine(%q) ok=%v, want %v", c.line, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if s.PID != c.pid {
			t.Errorf("pid = %d, want %d", s.PID, c.pid)
		}
		if s.RSSKiB != c.rss {
			t.Errorf("rss = %d, want %d", s.RSSKiB, c.rss)
		}
	}
}

func TestSamplerFakeRunner(t *testing.T) {
	c := NewCollector()
	// Fake ps output: two processes.
	fakePS := "  100  1024  4096  0.5\n  200  2048  8192  1.0\n"
	run := func(name string, args ...string) ([]byte, error) {
		if name == "ps" && strings.Contains(strings.Join(args, " "), "pid=") {
			return []byte(fakePS), nil
		}
		return nil, nil
	}
	s := NewSampler(c, time.Hour, run) // long interval so it doesn't auto-fire
	// Manually invoke sample.
	s.sample(time.Now())

	if got := c.Recent(100, 10); len(got) != 1 {
		t.Errorf("PID 100: expected 1 sample, got %d", len(got))
	}
	if got := c.Recent(200, 10); len(got) != 1 {
		t.Errorf("PID 200: expected 1 sample, got %d", len(got))
	}
}

func TestSamplerRunCancellation(t *testing.T) {
	c := NewCollector()
	s := NewSampler(c, 50*time.Millisecond, func(string, ...string) ([]byte, error) {
		return []byte("1 100 200 0.1\n"), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx) // should return when ctx times out
	// Verify at least one sample was collected.
	if len(c.PIDs()) == 0 {
		t.Error("expected at least one sample from sampler")
	}
}
