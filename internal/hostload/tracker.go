package hostload

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// Tracker defaults: one reading a second, enough history for idle's 5 second
// default window plus slack, and no work once nobody has asked for 10 minutes.
const (
	DefaultTrackInterval = time.Second
	DefaultTrackKeep     = 12 * time.Second
	DefaultTrackIdle     = 10 * time.Minute
)

// Baseline is one earlier reading of every process's cumulative CPU.
type Baseline struct {
	At    time.Time
	times map[int]cpuTimes
}

// Tracker keeps recent /proc CPU readings so a long-lived process (the daemon,
// spectra-mcp) can answer a windowed CPU question by diffing against a reading
// it already holds instead of sleeping through the window. It reads only while
// in use: the first Baseline call starts a reading loop that stops once
// nobody has asked for Idle. Off Linux it does nothing.
type Tracker struct {
	Host Host
	// ReadDir lists /proc; nil uses os.ReadDir.
	ReadDir  func(string) ([]os.DirEntry, error)
	Now      func() time.Time
	Interval time.Duration
	Keep     time.Duration
	Idle     time.Duration
	// NoLoop leaves reading to Record, for tests.
	NoLoop bool

	mu      sync.Mutex
	ring    []Baseline
	lastUse time.Time
	running bool
}

// NewTracker reads the real host with the default cadence.
func NewTracker() *Tracker {
	return &Tracker{Now: time.Now, Interval: DefaultTrackInterval, Keep: DefaultTrackKeep, Idle: DefaultTrackIdle}
}

// Baseline returns the newest held reading at least window old, provided it
// is no older than window plus slack, so the measured window stays close to
// the one asked for. ok is false when none qualifies; the caller then
// samples the slow way while the started loop warms up for the next call.
func (t *Tracker) Baseline(window time.Duration) (Baseline, bool) {
	if t == nil || t.Host.os() != hostos.Linux || window <= 0 {
		return Baseline{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	t.lastUse = now
	t.startLocked()
	maxAge := window + max(2*t.interval(), window/2)
	for i := len(t.ring) - 1; i >= 0; i-- {
		age := now.Sub(t.ring[i].At)
		if age < window {
			continue
		}
		if age <= maxAge {
			return t.ring[i], true
		}
		break
	}
	return Baseline{}, false
}

// Record takes one reading now and drops readings older than Keep.
func (t *Tracker) Record() {
	b := Baseline{times: t.readAll()}
	t.mu.Lock()
	defer t.mu.Unlock()
	b.At = t.Now()
	cut := 0
	for cut < len(t.ring) && b.At.Sub(t.ring[cut].At) > t.keep() {
		cut++
	}
	t.ring = append(t.ring[cut:], b)
}

func (t *Tracker) startLocked() {
	if t.running || t.NoLoop {
		return
	}
	t.running = true
	go t.loop()
}

func (t *Tracker) loop() {
	tick := time.NewTicker(t.interval())
	defer tick.Stop()
	for {
		t.Record()
		<-tick.C
		t.mu.Lock()
		if t.Now().Sub(t.lastUse) > t.idle() {
			t.running = false
			t.ring = nil
			t.mu.Unlock()
			return
		}
		t.mu.Unlock()
	}
}

func (t *Tracker) readAll() map[int]cpuTimes {
	readDir := t.ReadDir
	if readDir == nil {
		readDir = os.ReadDir
	}
	entries, err := readDir("/proc")
	if err != nil {
		return nil
	}
	out := make(map[int]cpuTimes, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if ct, ok := t.Host.readTimes(pid); ok {
			out[pid] = ct
		}
	}
	return out
}

func (t *Tracker) interval() time.Duration {
	if t.Interval > 0 {
		return t.Interval
	}
	return DefaultTrackInterval
}

func (t *Tracker) keep() time.Duration {
	if t.Keep > 0 {
		return t.Keep
	}
	return DefaultTrackKeep
}

func (t *Tracker) idle() time.Duration {
	if t.Idle > 0 {
		return t.Idle
	}
	return DefaultTrackIdle
}
