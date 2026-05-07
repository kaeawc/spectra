package telemetry

import (
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultLiveRetainWindow is the in-memory replay window used by live UI clients.
	DefaultLiveRetainWindow = 30 * time.Minute
	defaultLiveMaxSamples   = int(DefaultLiveRetainWindow / time.Second)
)

// Subject identifies what a live telemetry sample describes. JVMs, Electron
// apps, native apps, helpers, and future runtime collectors can all map to this.
type Subject struct {
	Kind     string `json:"kind"` // process, app, helper, runtime
	Runtime  string `json:"runtime,omitempty"`
	PID      int    `json:"pid,omitempty"`
	BundleID string `json:"bundle_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
}

func (s Subject) Key() string {
	switch {
	case s.Kind != "" && s.PID > 0:
		return fmt.Sprintf("%s:pid:%d", s.Kind, s.PID)
	case s.BundleID != "":
		return "bundle:" + s.BundleID
	case s.Path != "":
		return "path:" + s.Path
	case s.Name != "":
		return "name:" + s.Name
	default:
		return "unknown"
	}
}

// LiveSample is implemented by runtime-specific chart samples.
type LiveSample interface {
	TelemetrySubject() Subject
	TelemetryTakenAt() time.Time
}

type liveRing struct {
	samples [defaultLiveMaxSamples]LiveSample
	head    int
	count   int
}

func (r *liveRing) add(s LiveSample) {
	r.samples[r.head] = s
	r.head = (r.head + 1) % defaultLiveMaxSamples
	if r.count < defaultLiveMaxSamples {
		r.count++
	}
}

func (r *liveRing) recent(limit int) []LiveSample {
	if r.count == 0 {
		return nil
	}
	out := make([]LiveSample, r.count)
	if r.count < defaultLiveMaxSamples {
		copy(out, r.samples[:r.count])
	} else {
		n := copy(out, r.samples[r.head:])
		copy(out[n:], r.samples[:r.head])
	}
	if limit > 0 && len(out) > limit {
		return out[len(out)-limit:]
	}
	return out
}

func (r *liveRing) newest() (LiveSample, bool) {
	samples := r.recent(1)
	if len(samples) == 0 {
		return nil, false
	}
	return samples[0], true
}

// LiveCollector manages live telemetry rings keyed by subject.
type LiveCollector struct {
	mu       sync.RWMutex
	subjects map[string]*liveRing
}

func NewLiveCollector() *LiveCollector {
	return &LiveCollector{subjects: make(map[string]*liveRing)}
}

func (c *LiveCollector) Add(s LiveSample) {
	if s == nil {
		return
	}
	key := s.TelemetrySubject().Key()
	c.mu.Lock()
	rb := c.subjects[key]
	if rb == nil {
		rb = &liveRing{}
		c.subjects[key] = rb
	}
	rb.add(s)
	c.mu.Unlock()
}

func (c *LiveCollector) Recent(subject Subject, limit int) []LiveSample {
	return c.RecentKey(subject.Key(), limit)
}

func (c *LiveCollector) RecentKey(key string, limit int) []LiveSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	rb := c.subjects[key]
	if rb == nil {
		return nil
	}
	return rb.recent(limit)
}

func (c *LiveCollector) RecentPID(kind string, pid, limit int) []LiveSample {
	return c.Recent(Subject{Kind: kind, PID: pid}, limit)
}

func (c *LiveCollector) RecentAll(limit int) map[string][]LiveSample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string][]LiveSample, len(c.subjects))
	for key, rb := range c.subjects {
		if samples := rb.recent(limit); len(samples) > 0 {
			out[key] = samples
		}
	}
	return out
}

func (c *LiveCollector) Flush(retainWindow time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cutoff := now.UTC().Add(-retainWindow)
	for key, rb := range c.subjects {
		newest, ok := rb.newest()
		if ok && newest.TelemetryTakenAt().Before(cutoff) {
			delete(c.subjects, key)
		}
	}
}
