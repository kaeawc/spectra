// Package diskusage walks a directory tree and reports the largest
// directories and files by allocated (on-disk) size.
package diskusage

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Options controls a walk. The zero value reports the top 15 entries with
// no depth limit, staying on the root's filesystem.
type Options struct {
	Top         int    // entries per list; <= 0 means 15
	Depth       int    // deepest directory level reported (root's children are 1); <= 0 means unlimited
	MinSize     uint64 // omit reported entries smaller than this many allocated bytes
	CrossMounts bool   // descend into directories on other filesystems
	Now         func() time.Time
}

// DirEntry is a reported directory with its cumulative allocated size.
type DirEntry struct {
	Path           string `json:"path"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
}

// FileEntry is a reported file. ApparentBytes is set only when it differs
// from the allocation by more than 10% (sparse or compressed files).
type FileEntry struct {
	Path           string `json:"path"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
	ApparentBytes  uint64 `json:"apparent_bytes,omitempty"`
}

// Unreadable records a path that could not be read.
type Unreadable struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// Skipped records a directory that was deliberately not descended into.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

const (
	reasonOtherFS = "different filesystem"
	reasonVisited = "already visited"
	readBatch     = 1024
)

// Result is the outcome of a walk.
type Result struct {
	Root         string       `json:"root"`
	ResolvedRoot string       `json:"resolved_root,omitempty"`
	Skipped      []Skipped    `json:"skipped"`
	TotalBytes   uint64       `json:"total_bytes"`
	FileCount    int64        `json:"file_count"`
	DirCount     int64        `json:"dir_count"`
	Dirs         []DirEntry   `json:"top_dirs"`
	Files        []FileEntry  `json:"top_files"`
	Unreadable   []Unreadable `json:"unreadable"`
	Partial      bool         `json:"partial"`
	ElapsedMS    int64        `json:"elapsed_ms"`
}

type fileStat struct {
	alloc uint64
	dev   uint64
	ino   uint64
	nlink uint64
	ok    bool
}

type inodeKey struct{ dev, ino uint64 }

type walker struct {
	ctx     context.Context
	opts    Options
	rootDev uint64
	devOK   bool
	statFn  func(os.FileInfo) fileStat
	openDir func(string) (dirHandle, error)
	seen    map[inodeKey]struct{}
	visited map[inodeKey]struct{}
	dirs    *topN[DirEntry]
	files   *topN[FileEntry]
	res     *Result
}

// Walk measures root. A missing root is an error; everything below it that
// cannot be read is recorded in Result.Unreadable. If ctx is cancelled the
// partial result is returned with Partial set.
func Walk(ctx context.Context, root string, opts Options) (*Result, error) {
	return walkWith(ctx, root, opts, statOf)
}

// dirHandle is the part of an open directory the walk uses.
type dirHandle interface {
	ReadDir(n int) ([]fs.DirEntry, error)
	Close() error
}

func osOpenDir(path string) (dirHandle, error) { return os.Open(path) }

func walkWith(ctx context.Context, root string, opts Options, statFn func(os.FileInfo) fileStat) (*Result, error) {
	return walkOpen(ctx, root, opts, statFn, osOpenDir)
}

func walkOpen(ctx context.Context, root string, opts Options, statFn func(os.FileInfo) fileStat, openFn func(string) (dirHandle, error)) (*Result, error) {
	if opts.Top <= 0 {
		opts.Top = 15
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	start := now()

	// The root is user-named, so following it (os.Stat) is not following a
	// symlink found during the walk. Entries keep the typed prefix.
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}

	w := &walker{
		ctx:     ctx,
		opts:    opts,
		statFn:  statFn,
		openDir: openFn,
		seen:    map[inodeKey]struct{}{},
		visited: map[inodeKey]struct{}{},
		dirs: newTopN(opts.Top, func(a, b DirEntry) bool {
			return less(a.AllocatedBytes, b.AllocatedBytes, a.Path, b.Path)
		}),
		files: newTopN(opts.Top, func(a, b FileEntry) bool {
			return less(a.AllocatedBytes, b.AllocatedBytes, a.Path, b.Path)
		}),
		res: &Result{Root: root, Unreadable: []Unreadable{}, Skipped: []Skipped{}},
	}
	// Children are opened under the resolved root: joining onto the typed
	// root would lexically cancel a ".." that follows a symlink.
	realRoot := root
	if resolved, rerr := ResolveRoot(root); rerr == nil {
		realRoot = resolved
		if resolved != filepath.Clean(root) {
			w.res.ResolvedRoot = resolved
		}
	}
	st := statFn(fi)
	w.rootDev, w.devOK = st.dev, st.ok

	if fi.IsDir() {
		if st.ok {
			w.visited[inodeKey{st.dev, st.ino}] = struct{}{}
		}
		w.res.TotalBytes = w.walkDir(root, realRoot, fi, 0)
	} else {
		w.res.TotalBytes = w.addFile(root, fi, st)
	}

	w.res.Dirs = w.dirs.sorted()
	w.res.Files = w.files.sorted()
	w.res.Partial = ctx.Err() != nil
	w.res.ElapsedMS = now().Sub(start).Milliseconds()
	return w.res, nil
}

// ResolveRoot returns the real absolute path of root. EvalSymlinks cleans
// ".." lexically first, which is wrong after a symlink, so ".." components
// are applied to the already-resolved prefix instead.
func ResolveRoot(root string) (string, error) {
	cur, err := filepath.Abs(".")
	if err == nil {
		cur, err = filepath.EvalSymlinks(cur)
	}
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	if filepath.IsAbs(root) {
		cur = string(filepath.Separator)
	}
	for _, c := range strings.Split(root, string(filepath.Separator)) {
		switch c {
		case "", ".":
		case "..":
			cur = filepath.Dir(cur)
		default:
			if cur, err = filepath.EvalSymlinks(filepath.Join(cur, c)); err != nil {
				return "", fmt.Errorf("resolve %s: %w", root, err)
			}
		}
	}
	return cur, nil
}

// less orders ascending by size, then descending by path so that the
// min-heap evicts a deterministic entry on ties.
func less(sa, sb uint64, pa, pb string) bool {
	if sa != sb {
		return sa < sb
	}
	return pa > pb
}

// walkDir takes both the display path and the real path used for I/O.
func (w *walker) walkDir(path, realPath string, fi os.FileInfo, depth int) uint64 {
	w.res.DirCount++
	total := w.statFn(fi).alloc
	total += w.readEntries(path, realPath, depth)

	if depth > 0 && (w.opts.Depth <= 0 || depth <= w.opts.Depth) && total >= w.opts.MinSize {
		w.dirs.push(DirEntry{Path: path, AllocatedBytes: total})
	}
	return total
}

// readEntries reads a directory in batches so cancellation is prompt, and
// closes the handle before recursing so open descriptors do not grow with
// tree depth. Output order does not depend on entry order: the top-N
// comparison breaks ties by path.
func (w *walker) readEntries(path, realPath string, depth int) uint64 {
	entries, ok := w.listDir(path, realPath)
	if !ok {
		return 0
	}
	var total uint64
	for _, e := range entries {
		if w.ctx.Err() != nil {
			break
		}
		total += w.walkEntry(path, realPath, e, depth)
	}
	return total
}

func (w *walker) listDir(path, realPath string) ([]fs.DirEntry, bool) {
	f, err := w.openDir(realPath)
	if err != nil {
		w.res.Unreadable = append(w.res.Unreadable, Unreadable{Path: path, Error: err.Error()})
		return nil, false
	}
	defer f.Close()
	var all []fs.DirEntry
	for w.ctx.Err() == nil {
		entries, err := f.ReadDir(readBatch)
		all = append(all, entries...)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				w.res.Unreadable = append(w.res.Unreadable, Unreadable{Path: path, Error: err.Error()})
			}
			break
		}
	}
	return all, true
}

func (w *walker) walkEntry(parent, realParent string, e fs.DirEntry, depth int) uint64 {
	p := filepath.Join(parent, e.Name())
	rp := filepath.Join(realParent, e.Name())
	fi, err := e.Info() // lstat semantics: symlinks are not followed
	if err != nil {
		w.res.Unreadable = append(w.res.Unreadable, Unreadable{Path: p, Error: err.Error()})
		return 0
	}
	st := w.statFn(fi)
	if fi.IsDir() {
		if !w.opts.CrossMounts && w.devOK && st.ok && st.dev != w.rootDev {
			w.res.Skipped = append(w.res.Skipped, Skipped{Path: p, Reason: reasonOtherFS})
			return 0
		}
		if st.ok {
			k := inodeKey{st.dev, st.ino}
			if _, dup := w.visited[k]; dup {
				w.res.Skipped = append(w.res.Skipped, Skipped{Path: p, Reason: reasonVisited})
				return 0
			}
			w.visited[k] = struct{}{}
		}
		return w.walkDir(p, rp, fi, depth+1)
	}
	return w.addFile(p, fi, st)
}

func (w *walker) addFile(p string, fi os.FileInfo, st fileStat) uint64 {
	if st.ok && st.nlink > 1 {
		k := inodeKey{st.dev, st.ino}
		if _, dup := w.seen[k]; dup {
			return 0
		}
		w.seen[k] = struct{}{}
	}
	w.res.FileCount++
	if st.alloc >= w.opts.MinSize {
		fe := FileEntry{Path: p, AllocatedBytes: st.alloc}
		if apparent := widen(fi.Size()); differsBy10Pct(st.alloc, apparent) {
			fe.ApparentBytes = apparent
		}
		w.files.push(fe)
	}
	return st.alloc
}

// widen converts a stat field of whatever width this OS uses to uint64,
// clamping negatives to zero. Generic so the per-OS field types need no
// per-site casts.
func widen[T ~int32 | ~int64 | ~uint16 | ~uint32 | ~uint64](v T) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func differsBy10Pct(alloc, apparent uint64) bool {
	base, small := alloc, apparent
	if apparent > base {
		base, small = apparent, alloc
	}
	return base > 0 && (base-small)*10 > base
}

// topN keeps the n largest items using a bounded min-heap.
type topN[T any] struct {
	n int
	h *itemHeap[T]
}

func newTopN[T any](n int, lessFn func(a, b T) bool) *topN[T] {
	return &topN[T]{n: n, h: &itemHeap[T]{less: lessFn}}
}

func (t *topN[T]) push(v T) {
	if t.h.Len() < t.n {
		heap.Push(t.h, v)
		return
	}
	if t.h.less(t.h.items[0], v) {
		t.h.items[0] = v
		heap.Fix(t.h, 0)
	}
}

// sorted returns items largest first.
func (t *topN[T]) sorted() []T {
	out := append([]T{}, t.h.items...)
	sort.Slice(out, func(i, j int) bool { return t.h.less(out[j], out[i]) })
	return out
}

type itemHeap[T any] struct {
	items []T
	less  func(a, b T) bool
}

func (h *itemHeap[T]) Len() int           { return len(h.items) }
func (h *itemHeap[T]) Less(i, j int) bool { return h.less(h.items[i], h.items[j]) }
func (h *itemHeap[T]) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *itemHeap[T]) Push(x any)         { h.items = append(h.items, x.(T)) }
func (h *itemHeap[T]) Pop() any {
	old := h.items
	v := old[len(old)-1]
	h.items = old[:len(old)-1]
	return v
}
