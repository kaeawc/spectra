// Package metrics implements the live data ring buffer described in
// docs/operations/daemon.md and docs/design/storage.md (Tier 3).
//
// Process-level metrics are sampled at ~1Hz. The last 5 minutes per
// process are kept in RAM as a circular buffer. When the buffer wraps
// or the daemon requests a flush, samples are aggregated to 1-minute
// rows and written to SQLite.
package metrics

import (
	"sync"
	"time"
)

// maxSamples is 5 minutes at 1Hz.
const maxSamples = 300

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

// RingBuffer holds the most recent samples for one PID in a circular buffer.
type RingBuffer struct {
	samples [maxSamples]Sample
	head    int // index where next sample will be written
	count   int // total samples stored (≤ maxSamples)
}

// Add inserts a sample, overwriting the oldest when the buffer is full.
func (r *RingBuffer) Add(s Sample) {
	r.samples[r.head] = s
	r.head = (r.head + 1) % maxSamples
	if r.count < maxSamples {
		r.count++
	}
}

// Samples returns stored samples in chronological order (oldest first).
func (r *RingBuffer) Samples() []Sample {
	if r.count == 0 {
		return nil
	}
	out := make([]Sample, r.count)
	if r.count < maxSamples {
		copy(out, r.samples[:r.count])
		return out
	}
	// Buffer is full: oldest is at head.
	n := copy(out, r.samples[r.head:])
	copy(out[n:], r.samples[:r.head])
	return out
}

// Aggregate computes 1-minute aggregates from the stored samples.
func (r *RingBuffer) Aggregate() []Aggregate {
	samples := r.Samples()
	if len(samples) == 0 {
		return nil
	}
	// Group by truncated minute.
	type key struct {
		pid    int
		minute time.Time
	}
	groups := make(map[key]*Aggregate)
	for _, s := range samples {
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
	rb := c.pids[pid]
	c.mu.RUnlock()
	if rb == nil {
		return nil
	}
	all := rb.Samples()
	if limit > 0 && len(all) > limit {
		return all[len(all)-limit:]
	}
	return all
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

// FlushAggregates returns all 1-minute aggregates and removes PIDs whose
// ring buffers only contain samples older than retainWindow from RAM.
func (c *Collector) FlushAggregates(retainWindow time.Duration) []Aggregate {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().UTC().Add(-retainWindow)
	var all []Aggregate
	for pid, rb := range c.pids {
		aggs := rb.Aggregate()
		all = append(all, aggs...)
		// Evict PIDs whose newest sample is older than the retain window.
		samples := rb.Samples()
		if len(samples) > 0 && samples[len(samples)-1].TakenAt.Before(cutoff) {
			delete(c.pids, pid)
		}
	}
	return all
}
