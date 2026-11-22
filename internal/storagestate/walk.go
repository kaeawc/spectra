package storagestate

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// readDirBatch bounds how many entries one ReadDir call materializes, so a
// directory with millions of entries does not balloon memory.
const readDirBatch = 512

// maxWalkWorkers caps directory-reading concurrency, further bounded by
// GOMAXPROCS. Measured on a real ~/Library, more readers than this mostly
// add kernel contention (sys time) without shortening the walk.
const maxWalkWorkers = 4

// walkWorkers bounds limit by GOMAXPROCS, never below one.
func walkWorkers(limit int) int {
	return max(1, min(limit, runtime.GOMAXPROCS(0)))
}

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
	// lstatOnly disables the platform bulk directory reader, forcing one
	// lstat per entry. Tests use it to compare the two paths.
	lstatOnly bool

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []walkDir
	pending int // directories queued or being read

	seenMu sync.Mutex
	seen   map[seenKey]struct{}

	resMu sync.Mutex
	res   treeUsage
}

// walkScratch is one worker's reusable state, so reading a directory
// allocates only for the subdirectories it queues.
type walkScratch struct {
	tracked []int64
	dirs    []walkDir
	bulk    bulkScratch
}

// walkTree returns the on-disk usage of root, plus the usage beneath each
// tracked path that lies under root, all from one traversal. A missing root
// yields a zero result.
func walkTree(root string, tracked ...string) treeUsage {
	return walkTreeWith(false, root, tracked...)
}

// walkTreeWith is walkTree with the bulk directory reader optionally
// disabled.
func walkTreeWith(lstatOnly bool, root string, tracked ...string) treeUsage {
	return walkTreeWorkers(lstatOnly, 0, root, tracked...)
}

// walkTreeWorkers is walkTreeWith with an explicit reader count, for
// benchmarks; workers <= 0 picks the default.
func walkTreeWorkers(lstatOnly bool, workers int, root string, tracked ...string) treeUsage {
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

	w := &treeWalker{root: root, tracked: tracked, lstatOnly: lstatOnly, seen: map[seenKey]struct{}{}, res: res}
	w.cond = sync.NewCond(&w.mu)
	w.queue = []walkDir{{path: root, tracked: w.trackedAt(root, nil)}}
	w.pending = 1

	if workers <= 0 {
		workers = walkWorkers(maxWalkWorkers)
	}
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
	s := &walkScratch{tracked: make([]int64, len(w.tracked))}
	defer s.bulk.release()
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
		w.queue[len(w.queue)-1] = walkDir{}
		w.queue = w.queue[:len(w.queue)-1]
		w.mu.Unlock()

		w.readDir(d, s)

		w.mu.Lock()
		w.pending--
		if w.pending == 0 {
			w.cond.Broadcast()
		}
		w.mu.Unlock()
	}
}

// push queues dirs (copying them, so the caller may reuse the slice).
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

// pushScratch queues s.dirs and resets it for reuse.
func (w *treeWalker) pushScratch(s *walkScratch) {
	w.push(s.dirs)
	clear(s.dirs)
	s.dirs = s.dirs[:0]
}

// child derives the queue item for subdirectory name of d. The name is
// copied into the new path, so it may alias a reader's buffer.
func (w *treeWalker) child(d walkDir, name []byte) walkDir {
	path := joinName(d.path, name)
	top := d.top
	if d.path == w.root {
		top = path[len(path)-len(name):]
	}
	return walkDir{path: path, top: top, tracked: w.trackedAt(path, d.tracked)}
}

// joinName appends one directory entry name, which never contains a
// separator, to a clean directory path: filepath.Join for such inputs,
// without re-cleaning the path or a separate copy of name.
func joinName(dir string, name []byte) string {
	if strings.HasSuffix(dir, "/") {
		return dir + string(name)
	}
	return dir + "/" + string(name)
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
// It prefers the platform bulk reader and falls back to one lstat per entry
// when that reader is unavailable for the directory.
func (w *treeWalker) readDir(d walkDir, s *walkScratch) {
	clear(s.tracked)
	total, ok := int64(0), false
	if !w.lstatOnly {
		total, ok = w.readDirBulkWith(d, s)
	}
	if !ok {
		total = w.readDirLstat(d, s)
	}
	w.resMu.Lock()
	w.res.Total += total
	w.res.Top[d.top] += total
	for i, b := range s.tracked {
		w.res.Tracked[i] += b
	}
	w.resMu.Unlock()
}

// readDirLstat reads d with ReadDir and lstats each non-directory entry.
func (w *treeWalker) readDirLstat(d walkDir, s *walkScratch) int64 {
	f, err := os.Open(d.path)
	if err != nil {
		w.addSkipped()
		return 0
	}
	defer f.Close()

	var total int64
	for {
		entries, err := f.ReadDir(readDirBatch)
		for _, e := range entries {
			if e.IsDir() {
				s.dirs = append(s.dirs, w.child(d, []byte(e.Name())))
				continue
			}
			total += w.sizeEntry(e, d.tracked, s.tracked)
		}
		w.pushScratch(s)
		if err != nil {
			if err != io.EOF {
				w.addSkipped()
			}
			break
		}
	}
	return total
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
	return w.sizeInfo(fi, scopes, tracked)
}

// sizeInfo accounts for one lstat result.
func (w *treeWalker) sizeInfo(fi os.FileInfo, scopes []int, tracked []int64) int64 {
	var id fileID
	linked := false
	if fi.Mode().IsRegular() {
		id, linked = hardlinkID(fi)
	}
	return w.account(diskBytes(fi), id, linked, scopes, tracked)
}

// account adds b bytes to each tracked subtree in scopes and returns the
// whole-tree contribution. A linked inode counts once per scope.
func (w *treeWalker) account(b int64, id fileID, linked bool, scopes []int, tracked []int64) int64 {
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
