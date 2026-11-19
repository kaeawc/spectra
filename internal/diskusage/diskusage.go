// Package diskusage walks a directory tree and reports the largest
// directories and files by allocated (on-disk) size.
package diskusage

import (
	"container/heap"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Options controls a walk. The zero value reports the top 15 entries with
// no depth limit, staying on the root's filesystem.
type Options struct {
	Top         int   // entries per list; <= 0 means 15
	Depth       int   // deepest directory level reported (root's children are 1); <= 0 means unlimited
	MinSize     int64 // omit reported entries smaller than this many allocated bytes
	CrossMounts bool  // descend into directories on other filesystems
	Now         func() time.Time
}

// DirEntry is a reported directory with its cumulative allocated size.
type DirEntry struct {
	Path           string `json:"path"`
	AllocatedBytes int64  `json:"allocated_bytes"`
}

// FileEntry is a reported file. ApparentBytes is set only when it differs
// from the allocation by more than 10% (sparse or compressed files).
type FileEntry struct {
	Path           string `json:"path"`
	AllocatedBytes int64  `json:"allocated_bytes"`
	ApparentBytes  int64  `json:"apparent_bytes,omitempty"`
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
	TotalBytes   int64        `json:"total_bytes"`
	FileCount    int64        `json:"file_count"`
	DirCount     int64        `json:"dir_count"`
	Dirs         []DirEntry   `json:"top_dirs"`
	Files        []FileEntry  `json:"top_files"`
	Unreadable   []Unreadable `json:"unreadable"`
	Partial      bool         `json:"partial"`
	ElapsedMS    int64        `json:"elapsed_ms"`
}

type fileStat struct {
	alloc int64
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

func walkWith(ctx context.Context, root string, opts Options, statFn func(os.FileInfo) fileStat) (*Result, error) {
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
	if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil && resolved != filepath.Clean(root) {
		w.res.ResolvedRoot = resolved
	}
	st := statFn(fi)
	w.rootDev, w.devOK = st.dev, st.ok

	if fi.IsDir() {
		if st.ok {
			w.visited[inodeKey{st.dev, st.ino}] = struct{}{}
		}
		w.res.TotalBytes = w.walkDir(root, fi, 0)
	} else {
		w.res.TotalBytes = w.addFile(root, fi, st)
	}

	w.res.Dirs = w.dirs.sorted()
	w.res.Files = w.files.sorted()
	w.res.Partial = ctx.Err() != nil
	w.res.ElapsedMS = now().Sub(start).Milliseconds()
	return w.res, nil
}

// less orders ascending by size, then descending by path so that the
// min-heap evicts a deterministic entry on ties.
func less(sa, sb int64, pa, pb string) bool {
	if sa != sb {
		return sa < sb
	}
	return pa > pb
}

func (w *walker) walkDir(path string, fi os.FileInfo, depth int) int64 {
	w.res.DirCount++
	total := w.statFn(fi).alloc
	total += w.readEntries(path, depth)

	if depth > 0 && (w.opts.Depth <= 0 || depth <= w.opts.Depth) && total >= w.opts.MinSize {
		w.dirs.push(DirEntry{Path: path, AllocatedBytes: total})
	}
	return total
}

// readEntries reads path in batches so a huge directory neither sits in
// memory whole nor delays cancellation. Output order does not depend on
// entry order: the top-N comparison breaks ties by path.
func (w *walker) readEntries(path string, depth int) int64 {
	var total int64
	f, err := os.Open(path)
	if err != nil {
		w.res.Unreadable = append(w.res.Unreadable, Unreadable{Path: path, Error: err.Error()})
		return 0
	}
	defer f.Close()
	for w.ctx.Err() == nil {
		entries, err := f.ReadDir(readBatch)
		for _, e := range entries {
			if w.ctx.Err() != nil {
				return total
			}
			total += w.walkEntry(path, e, depth)
		}
		if err != nil {
			if err != io.EOF {
				w.res.Unreadable = append(w.res.Unreadable, Unreadable{Path: path, Error: err.Error()})
			}
			break
		}
	}
	return total
}

func (w *walker) walkEntry(parent string, e fs.DirEntry, depth int) int64 {
	p := filepath.Join(parent, e.Name())
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
		return w.walkDir(p, fi, depth+1)
	}
	return w.addFile(p, fi, st)
}

func (w *walker) addFile(p string, fi os.FileInfo, st fileStat) int64 {
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
		if differsBy10Pct(st.alloc, fi.Size()) {
			fe.ApparentBytes = fi.Size()
		}
		w.files.push(fe)
	}
	return st.alloc
}

func differsBy10Pct(alloc, apparent int64) bool {
	diff := alloc - apparent
	if diff < 0 {
		diff = -diff
	}
	base := alloc
	if apparent > base {
		base = apparent
	}
	return base > 0 && diff*10 > base
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
