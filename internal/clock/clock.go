// Package clock provides a small abstraction over wall-clock time so
// production code can take a dependency on Clock instead of calling
// time.Now directly. Tests substitute Fake to make time-dependent
// behavior deterministic.
package clock

import (
	"sync"
	"time"
)

// Clock reports wall-clock time. It deliberately does not handle
// scheduling (sleep, timers, tickers); use a separate abstraction
// when those are needed.
type Clock interface {
	// Now returns the current wall-clock time.
	Now() time.Time
}

// System is a Clock backed by time.Now.
type System struct{}

// Now returns time.Now().
func (System) Now() time.Time { return time.Now() }

// Default is a process-wide System clock for call sites that cannot
// reach a composition root. Prefer injecting a Clock explicitly when
// possible.
var Default Clock = System{}

// Fake is a manually-controlled Clock for tests. The zero value is
// usable and reports the Unix epoch in UTC.
//
// Fake is safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake whose Now reports start.
func NewFake(start time.Time) *Fake {
	return &Fake{now: start}
}

// Now returns the Fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set replaces the current time.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the current time forward by d. d may be negative.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
