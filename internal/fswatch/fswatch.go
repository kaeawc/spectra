// Package fswatch answers "what keeps writing here?" by snapshotting a
// directory tree on an interval and diffing consecutive snapshots into
// created, modified and removed events.
package fswatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// Entry kinds recorded for every tracked path.
const (
	EntryFile    = "file"
	EntryDir     = "dir"
	EntrySymlink = "symlink"
	EntryOther   = "other"
)

// Entry is one directory entry as reported by an FS. Symlinks must be
// reported with IsSymlink set and their own metadata, never the target's.
// IsOther marks devices, sockets, pipes and similar non-regular entries.
type Entry struct {
	Name      string
	IsDir     bool
	IsSymlink bool
	IsOther   bool
	Size      int64
	ModTime   time.Time
}

func entryKind(e Entry) string {
	switch {
	case e.IsSymlink:
		return EntrySymlink
	case e.IsDir:
		return EntryDir
	case e.IsOther:
		return EntryOther
	}
	return EntryFile
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
		m := info.Mode()
		out = append(out, Entry{
			Name:      de.Name(),
			IsDir:     m.IsDir(),
			IsSymlink: m&os.ModeSymlink != 0,
			IsOther:   !m.IsRegular() && !m.IsDir() && m&os.ModeSymlink == 0,
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
	Kind    string
}

// Snapshot is the tracked state of a tree. Entries are tracked in pre-order
// (a directory, then its children, names sorted per directory); when
// Truncated, Last is the final tracked path and everything after it in that
// order is untracked.
type Snapshot struct {
	Entries    map[string]Meta
	Unreadable map[string]bool
	Truncated  bool
	Last       string
}

type frame struct {
	dir     string
	entries []Entry
	idx     int
}

func sortedEntries(es []Entry) []Entry {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
	return es
}

// pathLess orders paths the way Take visits them: component-wise, so a
// directory precedes its children and "a.b" follows all of "a/...".
func pathLess(a, b string) bool {
	as := strings.Split(a, string(filepath.Separator))
	bs := strings.Split(b, string(filepath.Separator))
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

// Take walks root without following symlinks, tracking at most limit entries.
// Unreadable subdirectories are recorded in Unreadable; an unreadable root is
// an error. ctx is checked before every directory read, and a cancelled scan
// returns an error rather than a partial snapshot.
func Take(ctx context.Context, fsys FS, root string, limit int) (Snapshot, error) {
	snap := Snapshot{Entries: map[string]Meta{}, Unreadable: map[string]bool{}}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("scan interrupted: %w", err)
	}
	rootEntries, err := fsys.ReadDir(root) // handle is closed before we recurse
	if err != nil {
		return Snapshot{}, err
	}
	stack := []frame{{dir: root, entries: sortedEntries(rootEntries)}}
	for len(stack) > 0 {
		top := &stack[len(stack)-1]
		if top.idx >= len(top.entries) {
			stack = stack[:len(stack)-1]
			continue
		}
		e := top.entries[top.idx]
		top.idx++
		if len(snap.Entries) >= limit {
			snap.Truncated = true
			break
		}
		p := filepath.Join(top.dir, e.Name)
		kind := entryKind(e)
		snap.Entries[p] = Meta{Size: e.Size, ModTime: e.ModTime, Kind: kind}
		snap.Last = p
		if kind != EntryDir {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Snapshot{}, fmt.Errorf("scan interrupted: %w", err)
		}
		sub, err := fsys.ReadDir(p)
		if err != nil {
			snap.Unreadable[p] = true
			continue
		}
		stack = append(stack, frame{dir: p, entries: sortedEntries(sub)})
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("scan interrupted: %w", err)
	}
	return snap, nil
}

// Event is one observed change. Kind is created/modified/removed; EntryKind
// is the kind of entry at the path (the old kind for removed events).
type Event struct {
	Kind      string    `json:"event"`
	EntryKind string    `json:"kind"`
	Path      string    `json:"path"`
	Time      time.Time `json:"time"`
	Size      int64     `json:"size"`
}

// cutoff returns the last path both snapshots can vouch for. A truncated
// snapshot tracks exactly the paths up to its Last, so only paths at or
// before the smaller Last are comparable.
func cutoff(prev, next Snapshot) (string, bool) {
	var cut string
	limited := false
	for _, s := range []Snapshot{prev, next} {
		if !s.Truncated {
			continue
		}
		if !limited || pathLess(s.Last, cut) {
			cut = s.Last
		}
		limited = true
	}
	return cut, limited
}

func underAny(set map[string]bool, p string) bool {
	if len(set) == 0 {
		return false
	}
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if set[d] {
			return true
		}
		if parent := filepath.Dir(d); parent == d {
			return false
		}
	}
}

func eventRank(k string) int {
	switch k {
	case KindRemoved:
		return 0
	case KindCreated:
		return 1
	}
	return 2
}

// Diff compares two snapshots at time t, in path order. Directory mtime
// changes are ignored: they only echo child creates and removes. A change of
// entry kind at one path is a removal of the old kind followed by a creation
// of the new one. When either snapshot is truncated only paths up to the
// smaller cut-off are compared. Paths under a directory that was unreadable
// in the snapshot that would have to show them are not reported.
func Diff(prev, next Snapshot, t time.Time) []Event {
	cut, limited := cutoff(prev, next)
	tracked := func(p string) bool { return !limited || !pathLess(cut, p) }
	var evs []Event
	for p, m := range next.Entries {
		if !tracked(p) {
			continue
		}
		old, ok := prev.Entries[p]
		switch {
		case !ok:
			if !underAny(prev.Unreadable, p) {
				evs = append(evs, Event{Kind: KindCreated, EntryKind: m.Kind, Path: p, Time: t, Size: m.Size})
			}
		case old.Kind != m.Kind:
			evs = append(evs,
				Event{Kind: KindRemoved, EntryKind: old.Kind, Path: p, Time: t, Size: old.Size},
				Event{Kind: KindCreated, EntryKind: m.Kind, Path: p, Time: t, Size: m.Size})
		case m.Kind != EntryDir && (m.Size != old.Size || !m.ModTime.Equal(old.ModTime)):
			evs = append(evs, Event{Kind: KindModified, EntryKind: m.Kind, Path: p, Time: t, Size: m.Size})
		}
	}
	for p, old := range prev.Entries {
		if !tracked(p) {
			continue
		}
		if _, ok := next.Entries[p]; ok || underAny(next.Unreadable, p) {
			continue
		}
		evs = append(evs, Event{Kind: KindRemoved, EntryKind: old.Kind, Path: p, Time: t, Size: old.Size})
	}
	sort.Slice(evs, func(i, j int) bool {
		if evs[i].Path != evs[j].Path {
			return evs[i].Path < evs[j].Path
		}
		return eventRank(evs[i].Kind) < eventRank(evs[j].Kind)
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
	// HoldersIncomplete is set by the caller when holder lookup could not
	// see every holder (non-root, timed out, or interrupted).
	HoldersIncomplete bool `json:"holders_incomplete,omitempty"`
}

// Result is the outcome of Run.
type Result struct {
	Root        string   `json:"root"`
	DurationMS  int64    `json:"duration_ms"`
	IntervalMS  int64    `json:"interval_ms"`
	Polls       int      `json:"polls"`
	PollsFailed int      `json:"polls_failed"`
	Warnings    []string `json:"warnings,omitempty"`
	EndedReason string   `json:"ended_reason,omitempty"`
	Events      []Event  `json:"events"`
	Summary     Summary  `json:"summary"`
	Limits      []string `json:"limitations"`
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
	// Warn, if set, receives each warning as it is raised.
	Warn func(string)
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
// fails. A poll whose root cannot be read is skipped and counted in
// PollsFailed; a root that no longer exists ends the watch.
func Run(ctx context.Context, cfg Config) (Result, error) {
	start := cfg.Clock.Now()
	prev, err := Take(ctx, cfg.FS, cfg.Root, cfg.MaxEntries)
	if err != nil {
		return Result{}, err
	}
	res := Result{Root: cfg.Root, IntervalMS: cfg.Interval.Milliseconds(), Events: []Event{}, Limits: Limitations}
	w := &watcher{cfg: cfg, res: &res, acc: newAccumulator(), warned: map[string]bool{}}
	w.warn(prev)
	truncated := prev.Truncated
	for ctx.Err() == nil {
		remaining := cfg.Duration - cfg.Clock.Now().Sub(start)
		if remaining <= 0 {
			break
		}
		if err := cfg.Sleep(ctx, min(cfg.Interval, remaining)); err != nil {
			break
		}
		next, err := Take(ctx, cfg.FS, cfg.Root, cfg.MaxEntries)
		if err != nil {
			if ctx.Err() != nil {
				break // abandon the partial scan; do not diff it
			}
			if errors.Is(err, fs.ErrNotExist) {
				res.EndedReason = fmt.Sprintf("watched path no longer exists: %v", err)
				break
			}
			res.PollsFailed++
			continue
		}
		res.Polls++
		truncated = truncated || next.Truncated
		w.warn(next)
		w.record(Diff(prev, next, cfg.Clock.Now()))
		prev = next
	}
	res.DurationMS = cfg.Clock.Now().Sub(start).Milliseconds()
	res.Summary = w.acc.summary(cfg.Top, prev)
	res.Summary.EventsDropped = w.dropped
	res.Summary.Truncated = truncated
	if truncated {
		res.Summary.TruncationNote = fmt.Sprintf("tree exceeded --max-entries (%d); %d entries tracked; changes to entries beyond the cap are not seen", cfg.MaxEntries, len(prev.Entries))
	}
	return res, nil
}

type watcher struct {
	cfg     Config
	res     *Result
	acc     *accumulator
	warned  map[string]bool
	dropped int
}

// warn reports each unreadable directory once.
func (w *watcher) warn(snap Snapshot) {
	var fresh []string
	for d := range snap.Unreadable {
		if !w.warned[d] {
			w.warned[d] = true
			fresh = append(fresh, d)
		}
	}
	sort.Strings(fresh)
	for _, d := range fresh {
		msg := fmt.Sprintf("directory %s is unreadable; changes under it are not seen", d)
		w.res.Warnings = append(w.res.Warnings, msg)
		if w.cfg.Warn != nil {
			w.cfg.Warn(msg)
		}
	}
}

func (w *watcher) record(evs []Event) {
	for _, ev := range evs {
		w.acc.add(ev)
		if len(w.res.Events) < maxKeptEvents {
			w.res.Events = append(w.res.Events, ev)
		} else {
			w.dropped++
		}
		if w.cfg.Emit != nil {
			w.cfg.Emit(ev)
		}
	}
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
	if ev.EntryKind != EntryDir {
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
		// Tracked metadata is lstat-based, so a symlink is never a file.
		s.BusiestFiles[i].Exists = final.Entries[s.BusiestFiles[i].Path].Kind == EntryFile
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
