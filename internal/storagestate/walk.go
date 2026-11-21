package storagestate

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
)

// readDirBatch bounds how many entries one ReadDir call materializes, so a
// directory with millions of entries does not balloon memory.
const readDirBatch = 512

// maxWalkWorkers caps directory-reading concurrency. More than this mostly
// adds contention in the filesystem layer without improving throughput.
const maxWalkWorkers = 16

// treeUsage is the on-disk usage of one directory tree.
type treeUsage struct {
	// Total is the sparse-aware allocation of every non-directory entry
	// under the root, counting an inode with several hard links once.
	Total int64
	// Top splits Total by the root's immediate child directory name. Files
	// directly in the root are keyed "". An inode linked under two children
	// is attributed to whichever is seen first, so Top always sums to Total.
	Top map[string]int64
	// Tracked holds, per path passed to walkTree, the usage beneath it.
	// Each tracked subtree dedupes hardlinks only within itself, so an
	// inode also linked elsewhere still counts toward the subtree.
	Tracked []int64
	// SkippedDirs counts directories that could not be opened or fully
	// read (typically TCC-protected or permission-denied paths).
	SkippedDirs int
}

type walkDir struct {
	path    string
	top     string
	tracked []int // indexes into treeWalker.tracked; never mutated once queued
}

// seenKey scopes hardlink dedupe: scope -1 is the whole tree, i >= 0 is
// tracked subtree i.
type seenKey struct {
	scope int
	id    fileID
}

// treeWalker sizes a directory tree with a bounded pool of goroutines, each
// reading whole directories. Semantics match the filepath.Walk + Lstat sizing
// it replaces: symlinks are sized as links and never followed, directory
// entries themselves are not counted, and unreadable directories are
// skipped. Unlike Walk it counts a file with several hardlinks once, keyed by
// (device, inode), the way du does.
type treeWalker struct {
	root    string
	tracked []string

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []walkDir
	pending int // directories queued or being read

	seenMu sync.Mutex
	seen   map[seenKey]struct{}

	resMu sync.Mutex
	res   treeUsage
}

// walkTree returns the on-disk usage of root, plus the usage beneath each
// tracked path that lies under root, all from one traversal. A missing root
// yields a zero result.
func walkTree(root string, tracked ...string) treeUsage {
	root = filepath.Clean(root)
	tracked = slices.Clone(tracked)
	for i := range tracked {
		tracked[i] = filepath.Clean(tracked[i])
	}
	res := treeUsage{Top: map[string]int64{}, Tracked: make([]int64, len(tracked))}
	fi, err := os.Lstat(root)
	if err != nil {
		return res
	}
	if !fi.IsDir() {
		res.Total = diskBytes(fi)
		res.Top[""] = res.Total
		for i, p := range tracked {
			if p == root {
				res.Tracked[i] = res.Total
			}
		}
		return res
	}

	w := &treeWalker{root: root, tracked: tracked, seen: map[seenKey]struct{}{}, res: res}
	w.cond = sync.NewCond(&w.mu)
	w.queue = []walkDir{{path: root, tracked: w.trackedAt(root, nil)}}
	w.pending = 1

	workers := min(maxWalkWorkers, 2*runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.work()
		}()
	}
	wg.Wait()
	return w.res
}

func (w *treeWalker) work() {
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && w.pending > 0 {
			w.cond.Wait()
		}
		if w.pending == 0 {
			w.mu.Unlock()
			return
		}
		// LIFO keeps the queue shallow (depth-first) on wide trees.
		d := w.queue[len(w.queue)-1]
		w.queue = w.queue[:len(w.queue)-1]
		w.mu.Unlock()

		w.readDir(d)

		w.mu.Lock()
		w.pending--
		if w.pending == 0 {
			w.cond.Broadcast()
		}
		w.mu.Unlock()
	}
}

func (w *treeWalker) push(dirs []walkDir) {
	if len(dirs) == 0 {
		return
	}
	w.mu.Lock()
	w.queue = append(w.queue, dirs...)
	w.pending += len(dirs)
	w.mu.Unlock()
	if len(dirs) == 1 {
		w.cond.Signal()
	} else {
		w.cond.Broadcast()
	}
}

// child derives the queue item for subdirectory name of d.
func (w *treeWalker) child(d walkDir, name string) walkDir {
	path := filepath.Join(d.path, name)
	top := d.top
	if d.path == w.root {
		top = name
	}
	return walkDir{path: path, top: top, tracked: w.trackedAt(path, d.tracked)}
}

// trackedAt extends inherited with the tracked paths equal to path.
func (w *treeWalker) trackedAt(path string, inherited []int) []int {
	out := inherited
	for i, p := range w.tracked {
		if p == path {
			if len(out) == len(inherited) {
				out = slices.Clone(inherited)
			}
			out = append(out, i)
		}
	}
	return out
}

// readDir sizes the files directly inside d and queues its subdirectories.
func (w *treeWalker) readDir(d walkDir) {
	f, err := os.Open(d.path)
	if err != nil {
		w.addSkipped()
		return
	}
	defer f.Close()

	var total int64
	tracked := make([]int64, len(w.tracked))
	for {
		entries, err := f.ReadDir(readDirBatch)
		var dirs []walkDir
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, w.child(d, e.Name()))
				continue
			}
			total += w.sizeEntry(e, d.tracked, tracked)
		}
		w.push(dirs)
		if err != nil {
			if err != io.EOF {
				w.addSkipped()
			}
			break
		}
	}
	w.resMu.Lock()
	w.res.Total += total
	w.res.Top[d.top] += total
	for i, b := range tracked {
		w.res.Tracked[i] += b
	}
	w.resMu.Unlock()
}

// sizeEntry sizes one non-directory entry, adding it to each tracked subtree
// in scopes, and returns its contribution to the whole-tree total.
// DirEntry.Info lstats, so a symlink is sized as itself. An entry that
// vanished or cannot be stat'd counts as zero, as under filepath.Walk.
func (w *treeWalker) sizeEntry(e os.DirEntry, scopes []int, tracked []int64) int64 {
	fi, err := e.Info()
	if err != nil {
		return 0
	}
	b := diskBytes(fi)
	var id fileID
	linked := false
	if fi.Mode().IsRegular() {
		id, linked = hardlinkID(fi)
	}
	for _, i := range scopes {
		if !linked || w.firstSighting(seenKey{scope: i, id: id}) {
			tracked[i] += b
		}
	}
	if linked && !w.firstSighting(seenKey{scope: -1, id: id}) {
		return 0
	}
	return b
}

func (w *treeWalker) firstSighting(k seenKey) bool {
	w.seenMu.Lock()
	defer w.seenMu.Unlock()
	if _, dup := w.seen[k]; dup {
		return false
	}
	w.seen[k] = struct{}{}
	return true
}

func (w *treeWalker) addSkipped() {
	w.resMu.Lock()
	w.res.SkippedDirs++
	w.resMu.Unlock()
}
