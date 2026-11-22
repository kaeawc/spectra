// Package metrics implements the live data ring buffer described in
// docs/operations/daemon.md and docs/design/storage.md (Tier 3).
//
// Process-level metrics are sampled at ~1Hz. The last 30 minutes per
// process are kept in RAM as a circular buffer. When the buffer wraps
// or the daemon requests a flush, samples are aggregated to 1-minute
// rows and written to SQLite.
package metrics

import (
	"iter"
	"sync"
	"time"
)

// DefaultRetainWindow is the in-memory per-process replay window.
const DefaultRetainWindow = 30 * time.Minute

// maxSamples is DefaultRetainWindow at 1Hz: the per-process sample cap
// whatever the sampling interval.
const maxSamples = int(DefaultRetainWindow / time.Second)

// Sample is one point-in-time observation of a single process.
type Sample struct {
	TakenAt  time.Time `json:"taken_at"`
	PID      int       `json:"pid"`
	RSSKiB   int64     `json:"rss_kib"`
	CPUPct   float64   `json:"cpu_pct"` // from ps -o pcpu (average since process start)
	VSizeKiB int64     `json:"vsize_kib"`
}

// Aggregate is the 1-minute summary written to SQLite on flush.
type Aggregate struct {
	PID         int       `json:"pid"`
	MinuteAt    time.Time `json:"minute_at"` // truncated to the minute
	AvgRSSKiB   int64     `json:"avg_rss_kib"`
	MaxRSSKiB   int64     `json:"max_rss_kib"`
	AvgCPUPct   float64   `json:"avg_cpu_pct"`
	MaxCPUPct   float64   `json:"max_cpu_pct"`
	SampleCount int       `json:"sample_count"`
}

// RingBuffer holds the most recent samples for one PID: at most
// maxSamples, and none older than DefaultRetainWindow before the newest. It
// is a circular buffer sized to what it holds, so a process sampled every
// 15 s costs about 120 samples rather than a fixed 1 Hz array, and a
// short-lived one a handful.
type RingBuffer struct {
	buf  []Sample
	head int // index of the oldest sample
	n    int
	// flushed is the minute of the newest aggregate already returned by a
	// flush; later flushes return that minute (it may have been partial)
	// and newer ones only.
	flushed time.Time
}

const minRingCap = 4

// Add appends a sample after dropping samples beyond the count cap or older
// than the retain window relative to it. Dropping first lets a steady
// cadence reuse the slot it frees instead of growing the buffer.
func (r *RingBuffer) Add(s Sample) {
	cutoff := s.TakenAt.Add(-DefaultRetainWindow)
	for r.n > 0 && (r.n >= maxSamples || r.buf[r.head].TakenAt.Before(cutoff)) {
		r.buf[r.head] = Sample{}
		r.head = (r.head + 1) % len(r.buf)
		r.n--
	}
	switch {
	case r.n == len(r.buf):
		r.resize(min(max(minRingCap, 2*len(r.buf)), maxSamples))
	case len(r.buf) > 16*minRingCap && r.n < len(r.buf)/4:
		r.resize(max(minRingCap, 2*r.n)) // the cadence slowed or a gap emptied it; give back the space
	}
	r.buf[(r.head+r.n)%len(r.buf)] = s
	r.n++
}

func (r *RingBuffer) resize(size int) {
	nb := make([]Sample, size)
	a, b := r.from(0)
	copy(nb[copy(nb, a):], b)
	r.buf, r.head = nb, 0
}

// from returns the logical samples [i, n) as at most two views of buf.
func (r *RingBuffer) from(i int) ([]Sample, []Sample) {
	if i >= r.n {
		return nil, nil
	}
	start := (r.head + i) % len(r.buf)
	end := r.head + r.n
	if end <= len(r.buf) {
		return r.buf[start:end], nil
	}
	end -= len(r.buf)
	if start < r.head {
		return r.buf[start:end], nil
	}
	return r.buf[start:], r.buf[:end]
}

func (r *RingBuffer) at(i int) Sample { return r.buf[(r.head+i)%len(r.buf)] }

// Len is the number of samples held.
func (r *RingBuffer) Len() int { return r.n }

// Samples returns stored samples in chronological order (oldest first).
func (r *RingBuffer) Samples() []Sample {
	return r.tail(0)
}

// tail copies the newest limit samples (all when limit <= 0), oldest first.
func (r *RingBuffer) tail(limit int) []Sample {
	if r.n == 0 {
		return nil
	}
	i := 0
	if limit > 0 && r.n > limit {
		i = r.n - limit
	}
	a, b := r.from(i)
	out := make([]Sample, 0, len(a)+len(b))
	return append(append(out, a...), b...)
}

// Aggregate computes 1-minute aggregates from the stored samples.
func (r *RingBuffer) Aggregate() []Aggregate {
	return aggregate(r.from(0))
}

// aggregateSince aggregates the minutes at or after since (all when zero).
func (r *RingBuffer) aggregateSince(since time.Time) []Aggregate {
	i := 0
	if !since.IsZero() {
		i = r.n
		for i > 0 && !r.at(i-1).TakenAt.UTC().Truncate(time.Minute).Before(since) {
			i--
		}
	}
	return aggregate(r.from(i))
}

func iterSamples(parts ...[]Sample) iter.Seq[Sample] {
	return func(yield func(Sample) bool) {
		for _, part := range parts {
			for _, s := range part {
				if !yield(s) {
					return
				}
			}
		}
	}
}

func aggregate(first, second []Sample) []Aggregate {
	if len(first)+len(second) == 0 {
		return nil
	}
	// Group by truncated minute.
	type key struct {
		pid    int
		minute time.Time
	}
	groups := make(map[key]*Aggregate)
	for s := range iterSamples(first, second) {
		minute := s.TakenAt.UTC().Truncate(time.Minute)
		k := key{s.PID, minute}
		agg := groups[k]
		if agg == nil {
			agg = &Aggregate{PID: s.PID, MinuteAt: minute}
			groups[k] = agg
		}
		agg.SampleCount++
		agg.AvgRSSKiB += s.RSSKiB
		if s.RSSKiB > agg.MaxRSSKiB {
			agg.MaxRSSKiB = s.RSSKiB
		}
		agg.AvgCPUPct += s.CPUPct
		if s.CPUPct > agg.MaxCPUPct {
			agg.MaxCPUPct = s.CPUPct
		}
	}
	out := make([]Aggregate, 0, len(groups))
	for _, agg := range groups {
		if agg.SampleCount > 0 {
			agg.AvgRSSKiB /= int64(agg.SampleCount)
			agg.AvgCPUPct /= float64(agg.SampleCount)
		}
		out = append(out, *agg)
	}
	return out
}

// Collector manages per-PID ring buffers and is safe for concurrent use.
type Collector struct {
	mu   sync.RWMutex
	pids map[int]*RingBuffer
}

// NewCollector returns an empty Collector.
func NewCollector() *Collector {
	return &Collector{pids: make(map[int]*RingBuffer)}
}

// Add records one sample. A new ring buffer is created if this PID
// hasn't been seen before.
func (c *Collector) Add(s Sample) {
	c.mu.Lock()
	rb := c.pids[s.PID]
	if rb == nil {
		rb = &RingBuffer{}
		c.pids[s.PID] = rb
	}
	rb.Add(s)
	c.mu.Unlock()
}

// Recent returns the most recent samples for pid (up to limit).
// Returns nil if pid has no data.
func (c *Collector) Recent(pid, limit int) []Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rb := c.pids[pid]
	if rb == nil {
		return nil
	}
	return rb.tail(limit)
}

// PIDs returns the list of PIDs with any data.
func (c *Collector) PIDs() []int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]int, 0, len(c.pids))
	for pid := range c.pids {
		out = append(out, pid)
	}
	return out
}

// Len is the number of PIDs held.
func (c *Collector) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.pids)
}

// FlushAggregates returns the 1-minute aggregates not yet returned by an
// earlier flush (plus each PID's last flushed minute, which may have been
// partial), and removes PIDs whose newest sample is older than retainWindow
// before now. Each completed minute is therefore returned once rather than
// on every flush for as long as its samples stay in RAM.
func (c *Collector) FlushAggregates(now time.Time, retainWindow time.Duration) []Aggregate {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := now.UTC().Add(-retainWindow)
	var all []Aggregate
	for pid, rb := range c.pids {
		aggs := rb.aggregateSince(rb.flushed)
		for _, a := range aggs {
			if a.MinuteAt.After(rb.flushed) {
				rb.flushed = a.MinuteAt
			}
		}
		all = append(all, aggs...)
		if rb.n == 0 || rb.at(rb.n-1).TakenAt.Before(cutoff) {
			delete(c.pids, pid)
		}
	}
	return all
}
