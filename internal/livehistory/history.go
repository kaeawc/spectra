// Package livehistory keeps recent daemon live collector snapshots in memory.
package livehistory

import (
	"sync"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// DefaultCapacity is the daemon's in-memory replay window for broad live
// collector snapshots. At the current 1-minute cadence this keeps 30 minutes.
const DefaultCapacity = 30

// Ring stores recent snapshots in chronological order behind a circular buffer.
type Ring struct {
	mu        sync.RWMutex
	snapshots []snapshot.Snapshot
	head      int
	count     int
}

// NewRing returns a Ring that retains at most capacity snapshots.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Ring{snapshots: make([]snapshot.Snapshot, capacity)}
}

// Add records one snapshot, overwriting the oldest when the ring is full.
func (r *Ring) Add(s snapshot.Snapshot) {
	if r == nil || len(r.snapshots) == 0 {
		return
	}
	r.mu.Lock()
	r.snapshots[r.head] = s
	r.head = (r.head + 1) % len(r.snapshots)
	if r.count < len(r.snapshots) {
		r.count++
	}
	r.mu.Unlock()
}

// Recent returns snapshots in chronological order, capped to the newest limit
// when limit is positive.
func (r *Ring) Recent(limit int) []snapshot.Snapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.count == 0 {
		return nil
	}
	all := make([]snapshot.Snapshot, r.count)
	if r.count < len(r.snapshots) {
		copy(all, r.snapshots[:r.count])
	} else {
		n := copy(all, r.snapshots[r.head:])
		copy(all[n:], r.snapshots[:r.head])
	}
	if limit > 0 && len(all) > limit {
		return all[len(all)-limit:]
	}
	return all
}

// Latest returns the newest snapshot, if any.
func (r *Ring) Latest() (snapshot.Snapshot, bool) {
	recent := r.Recent(1)
	if len(recent) == 0 {
		return snapshot.Snapshot{}, false
	}
	return recent[0], true
}
