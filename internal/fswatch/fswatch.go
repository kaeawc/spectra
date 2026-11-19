// Package fswatch answers "what keeps writing here?" by snapshotting a
// directory tree on an interval and diffing consecutive snapshots into
// created, modified and removed events.
package fswatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

// Event kinds.
const (
	KindCreated  = "created"
	KindModified = "modified"
	KindRemoved  = "removed"
)

// DefaultMaxEntries bounds the tracked snapshot size.
const DefaultMaxEntries = 200000

// maxKeptEvents bounds the event list retained for the final result; counts
// and rankings still cover every event.
const maxKeptEvents = 50000

// Limitations are shown in text and JSON output.
var Limitations = []string{
	"polling misses files that appear and vanish between polls",
	"multiple writes within one interval count as one modification",
	"holders are the processes with the file open when the watch ended, not necessarily the writers",
}

// Entry is one directory entry as reported by an FS. Symlinks must be
// reported with IsSymlink set and their own metadata, never the target's.
type Entry struct {
	Name      string
	IsDir     bool
	IsSymlink bool
	Size      int64
	ModTime   time.Time
}

// FS is the filesystem seam. ReadDir must not follow symlinks for entries and
// must release any handle before returning.
type FS interface {
	ReadDir(path string) ([]Entry, error)
}

// OSFS is the real filesystem.
type OSFS struct{}

// ReadDir lists path with per-entry lstat-style metadata.
func (OSFS) ReadDir(path string) ([]Entry, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", path, err)
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue // vanished between listing and stat
		}
		out = append(out, Entry{
			Name:      de.Name(),
			IsDir:     info.IsDir(),
			IsSymlink: info.Mode()&os.ModeSymlink != 0,
			Size:      info.Size(),
			ModTime:   info.ModTime(),
		})
	}
	return out, nil
}

// Meta is the tracked state of one path.
type Meta struct {
	Size    int64
	ModTime time.Time
	IsDir   bool
}

// Snapshot is the tracked state of a tree.
type Snapshot struct {
	Entries   map[string]Meta
	Truncated bool
}

// Take walks root without following symlinks, tracking at most limit entries.
// Unreadable subdirectories are skipped; an unreadable root is an error.
func Take(fsys FS, root string, limit int) (Snapshot, error) {
	snap := Snapshot{Entries: map[string]Meta{}}
	stack := []string{root}
	first := true
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := fsys.ReadDir(dir) // handle is closed before we recurse
		if err != nil {
			if first {
				return Snapshot{}, err
			}
			continue
		}
		first = false
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		var subdirs []string
		for _, e := range entries {
			if len(snap.Entries) >= limit {
				snap.Truncated = true
				return snap, nil
			}
			p := filepath.Join(dir, e.Name)
			isDir := e.IsDir && !e.IsSymlink
			snap.Entries[p] = Meta{Size: e.Size, ModTime: e.ModTime, IsDir: isDir}
			if isDir {
				subdirs = append(subdirs, p)
			}
		}
		for i := len(subdirs) - 1; i >= 0; i-- {
			stack = append(stack, subdirs[i])
		}
	}
	return snap, nil
}

// Event is one observed change.
type Event struct {
	Kind  string    `json:"event"`
	Path  string    `json:"path"`
	Time  time.Time `json:"time"`
	Size  int64     `json:"size"`
	IsDir bool      `json:"is_dir,omitempty"`
}

// Diff compares two snapshots at time t, in path order. Directory mtime
// changes are ignored: they only echo child creates and removes. When next
// was truncated, removals are not reported because the cap, not the
// filesystem, may have hidden the path.
func Diff(prev, next Snapshot, t time.Time) []Event {
	var evs []Event
	for p, m := range next.Entries {
		old, ok := prev.Entries[p]
		switch {
		case !ok:
			evs = append(evs, Event{Kind: KindCreated, Path: p, Time: t, Size: m.Size, IsDir: m.IsDir})
		case !m.IsDir && !old.IsDir && (m.Size != old.Size || !m.ModTime.Equal(old.ModTime)):
			evs = append(evs, Event{Kind: KindModified, Path: p, Time: t, Size: m.Size})
		}
	}
	if !next.Truncated {
		for p, old := range prev.Entries {
			if _, ok := next.Entries[p]; !ok {
				evs = append(evs, Event{Kind: KindRemoved, Path: p, Time: t, Size: old.Size, IsDir: old.IsDir})
			}
		}
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].Path != evs[j].Path {
			return evs[i].Path < evs[j].Path
		}
		return evs[i].Kind < evs[j].Kind
	})
	return evs
}

// Holder is a process holding a path open.
type Holder struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Mode    string `json:"mode,omitempty"`
}

// Busy is a path with its event count.
type Busy struct {
	Path        string   `json:"path"`
	Events      int      `json:"events"`
	Exists      bool     `json:"exists,omitempty"`
	Holders     []Holder `json:"holders,omitempty"`
	HolderError string   `json:"holder_error,omitempty"`
}

// Counts totals events by kind.
type Counts struct {
	Created  int `json:"created"`
	Modified int `json:"modified"`
	Removed  int `json:"removed"`
}

// Summary aggregates a watch.
type Summary struct {
	Counts         Counts `json:"counts"`
	TotalEvents    int    `json:"total_events"`
	BusiestFiles   []Busy `json:"busiest_files"`
	BusiestDirs    []Busy `json:"busiest_dirs"`
	DistinctFiles  int    `json:"distinct_files"`
	DistinctDirs   int    `json:"distinct_dirs"`
	TrackedEntries int    `json:"tracked_entries"`
	Truncated      bool   `json:"truncated"`
	TruncationNote string `json:"truncation_note,omitempty"`
	EventsDropped  int    `json:"events_dropped,omitempty"`
}

// Result is the outcome of Run.
type Result struct {
	Root       string   `json:"root"`
	DurationMS int64    `json:"duration_ms"`
	IntervalMS int64    `json:"interval_ms"`
	Polls      int      `json:"polls"`
	Events     []Event  `json:"events"`
	Summary    Summary  `json:"summary"`
	Limits     []string `json:"limitations"`
}

// Config configures Run.
type Config struct {
	Root       string
	Interval   time.Duration
	Duration   time.Duration
	Top        int
	MaxEntries int
	FS         FS
	Clock      clock.Clock
	// Sleep waits d or returns an error if ctx ends first.
	Sleep func(ctx context.Context, d time.Duration) error
	// Emit, if set, receives each event as it is observed.
	Emit func(Event)
}

// RealSleep waits on a timer or the context.
func RealSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sleep interrupted: %w", ctx.Err())
	}
}

// Run watches Config.Root until Duration elapses or ctx ends, and returns the
// events and summary. The error is non-nil only when the initial snapshot
// fails.
func Run(ctx context.Context, cfg Config) (Result, error) {
	start := cfg.Clock.Now()
	prev, err := Take(cfg.FS, cfg.Root, cfg.MaxEntries)
	if err != nil {
		return Result{}, err
	}
	res := Result{Root: cfg.Root, IntervalMS: cfg.Interval.Milliseconds(), Events: []Event{}, Limits: Limitations}
	acc := newAccumulator()
	dropped := 0
	truncated := prev.Truncated
	for ctx.Err() == nil {
		remaining := cfg.Duration - cfg.Clock.Now().Sub(start)
		if remaining <= 0 {
			break
		}
		if err := cfg.Sleep(ctx, min(cfg.Interval, remaining)); err != nil {
			break
		}
		next, err := Take(cfg.FS, cfg.Root, cfg.MaxEntries)
		if err != nil {
			next = Snapshot{Entries: map[string]Meta{}} // root vanished: everything removed
		}
		res.Polls++
		truncated = truncated || next.Truncated
		for _, ev := range Diff(prev, next, cfg.Clock.Now()) {
			acc.add(ev)
			if len(res.Events) < maxKeptEvents {
				res.Events = append(res.Events, ev)
			} else {
				dropped++
			}
			if cfg.Emit != nil {
				cfg.Emit(ev)
			}
		}
		prev = next
	}
	res.DurationMS = cfg.Clock.Now().Sub(start).Milliseconds()
	res.Summary = acc.summary(cfg.Top, prev)
	res.Summary.EventsDropped = dropped
	res.Summary.Truncated = truncated
	if truncated {
		res.Summary.TruncationNote = fmt.Sprintf("tree exceeded --max-entries (%d); entries past the cap were not tracked and removals are not reported on truncated polls", cfg.MaxEntries)
	}
	return res, nil
}

type accumulator struct {
	counts Counts
	total  int
	files  map[string]int
	dirs   map[string]int
}

func newAccumulator() *accumulator {
	return &accumulator{files: map[string]int{}, dirs: map[string]int{}}
}

func (a *accumulator) add(ev Event) {
	switch ev.Kind {
	case KindCreated:
		a.counts.Created++
	case KindModified:
		a.counts.Modified++
	case KindRemoved:
		a.counts.Removed++
	}
	a.total++
	if !ev.IsDir {
		a.files[ev.Path]++
	}
	a.dirs[filepath.Dir(ev.Path)]++
}

func (a *accumulator) summary(top int, final Snapshot) Summary {
	s := Summary{
		Counts: a.counts, TotalEvents: a.total, TrackedEntries: len(final.Entries),
		DistinctFiles: len(a.files), DistinctDirs: len(a.dirs),
		BusiestFiles: rank(a.files, top), BusiestDirs: rank(a.dirs, top),
	}
	for i := range s.BusiestFiles {
		_, s.BusiestFiles[i].Exists = final.Entries[s.BusiestFiles[i].Path]
	}
	return s
}

func rank(m map[string]int, top int) []Busy {
	out := make([]Busy, 0, len(m))
	for p, n := range m {
		out = append(out, Busy{Path: p, Events: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Events != out[j].Events {
			return out[i].Events > out[j].Events
		}
		return out[i].Path < out[j].Path
	})
	if len(out) > top {
		out = out[:top]
	}
	return out
}
