package hostload

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

type dirEntry string

func (d dirEntry) Name() string               { return string(d) }
func (d dirEntry) IsDir() bool                { return true }
func (d dirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (d dirEntry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }

func (f *fakeProc) tracker(osk hostos.Kind) *Tracker {
	s := f.sampler(osk)
	return &Tracker{
		Host: s.Host,
		ReadDir: func(string) ([]os.DirEntry, error) {
			return []os.DirEntry{dirEntry("1"), dirEntry("2"), dirEntry("self"), dirEntry("meminfo")}, nil
		},
		Now:    s.Now,
		NoLoop: true,
	}
}

func TestSampleUsesTrackerBaselineWithoutSleeping(t *testing.T) {
	f := &fakeProc{
		now: time.Unix(1000, 0),
		files: map[string]string{
			"/proc/1/stat": statLine(1, 1000, 5),
			"/proc/2/stat": statLine(2, 9000, 6),
		},
		rounds: [][]process.Info{{{PID: 1, CPUPct: 0.07}, {PID: 2, CPUPct: 90}}},
	}
	tr := f.tracker(hostos.Linux)
	tr.Record()
	f.now = f.now.Add(time.Second)
	f.files["/proc/1/stat"] = statLine(1, 1050, 5)

	s := f.sampler(hostos.Linux)
	s.Sleep = func(context.Context, time.Duration) { t.Fatal("slept despite a usable baseline") }
	s.Baseline = tr.Baseline
	procs, window, basis := s.Sample(context.Background(), 500*time.Millisecond)
	if basis != BasisWindow || window != time.Second {
		t.Fatalf("basis=%s window=%v, want window over the 1s since the baseline", basis, window)
	}
	m := byPID(procs)
	if m[1].CPUPct != 50 || m[2].CPUPct != 0 {
		t.Fatalf("cpu = %v / %v, want 50 / 0", m[1].CPUPct, m[2].CPUPct)
	}
	if f.calls != 1 {
		t.Fatalf("collected %d times, want 1", f.calls)
	}
}

func TestTrackerBaselineAge(t *testing.T) {
	f := &fakeProc{now: time.Unix(1000, 0), files: map[string]string{"/proc/1/stat": statLine(1, 1, 1)}}
	tr := f.tracker(hostos.Linux)
	tr.Record() // t=1000
	f.now = f.now.Add(time.Second)
	tr.Record() // t=1001
	f.now = f.now.Add(300 * time.Millisecond)

	if _, ok := tr.Baseline(5 * time.Second); ok {
		t.Fatal("no reading is 5s old, want none")
	}
	b, ok := tr.Baseline(time.Second)
	if !ok || !b.At.Equal(time.Unix(1000, 0)) {
		t.Fatalf("baseline = %v %v, want the 1.3s-old reading", b.At, ok)
	}
	b, ok = tr.Baseline(200 * time.Millisecond)
	if !ok || !b.At.Equal(time.Unix(1001, 0)) {
		t.Fatalf("baseline = %v %v, want the newest reading old enough", b.At, ok)
	}
	f.now = f.now.Add(time.Minute)
	if _, ok := tr.Baseline(time.Second); ok {
		t.Fatal("readings a minute old are too stale for a 1s window")
	}
	if len(tr.ring[0].times) != 1 {
		t.Fatalf("reading = %v, want only numeric /proc entries", tr.ring[0].times)
	}
}

func TestTrackerRecordDropsOldReadings(t *testing.T) {
	f := &fakeProc{now: time.Unix(1000, 0), files: map[string]string{}}
	tr := f.tracker(hostos.Linux)
	tr.Keep = 3 * time.Second
	for range 10 {
		tr.Record()
		f.now = f.now.Add(time.Second)
	}
	if len(tr.ring) != 4 {
		t.Fatalf("held %d readings, want 4 within Keep", len(tr.ring))
	}
}

func TestTrackerOffLinux(t *testing.T) {
	f := &fakeProc{now: time.Unix(1000, 0), files: map[string]string{}}
	tr := f.tracker(hostos.Darwin)
	tr.Record()
	f.now = f.now.Add(time.Second)
	if _, ok := tr.Baseline(500 * time.Millisecond); ok {
		t.Fatal("tracker answered on macOS")
	}
	var nilTracker *Tracker
	if _, ok := nilTracker.Baseline(time.Second); ok {
		t.Fatal("nil tracker answered")
	}
}

func TestTrackerLoopStopsWhenUnused(t *testing.T) {
	tr := NewTracker()
	tr.Host = Host{OS: hostos.Linux, ReadFile: func(string) ([]byte, error) { return nil, errors.New("none") }}
	tr.ReadDir = func(string) ([]os.DirEntry, error) { return nil, nil }
	tr.Interval, tr.Idle = 5*time.Millisecond, 20*time.Millisecond
	tr.Baseline(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		running := tr.running
		tr.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reading loop still running long after its last use")
}
