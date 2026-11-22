package detect

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// treeRecord keeps the directory listings a tree walk read under root, so a
// later pass that needs filepath.WalkDir's view of root can replay them
// instead of listing every directory again. Only a walk that read every
// directory and lstat'd every entry under root without error is replayable;
// anything else is left to a real WalkDir, whose error callbacks the replay
// does not reproduce.
type treeRecord struct {
	root   string
	dirs   map[string]*treeListing
	failed bool
}

// treeListing is one directory's entries, in the order the walk read them.
type treeListing struct {
	rec     *treeRecord
	entries []treeEntry
}

// treeEntry is a recorded entry; it serves as the fs.DirEntry WalkDir would
// have passed for it.
type treeEntry struct {
	path, name string
	mode       fs.FileMode // type bits only
	size       int64       // lstat size; 0 for directories
}

func newTreeRecord(root string) *treeRecord {
	return &treeRecord{root: filepath.Clean(root), dirs: map[string]*treeListing{}}
}

// listing starts recording dir, or returns nil when dir is outside the
// recorded root (or rec is nil). Listing methods accept a nil receiver.
func (rec *treeRecord) listing(dir string) *treeListing {
	if rec == nil || (dir != rec.root && !strings.HasPrefix(dir, rec.root+"/")) {
		return nil
	}
	ls := &treeListing{rec: rec}
	rec.dirs[dir] = ls
	return ls
}

// add records the entry at path, whose parent directory path is
// path[:dirLen].
func (ls *treeListing) add(path string, dirLen int, mode fs.FileMode, size int64) {
	if ls != nil {
		ls.entries = append(ls.entries, treeEntry{path: path, name: path[dirLen+1:], mode: mode.Type(), size: size})
	}
}

// fail marks the record unreplayable: a directory or entry could not be read.
func (ls *treeListing) fail() {
	if ls != nil {
		ls.rec.failed = true
	}
}

// walkEntries returns the callbacks filepath.WalkDir(root) would make —
// root, then each directory's entries sorted by name, depth first — when
// root is the recorded root and the walk read all of it. ok is false
// otherwise. The listings are sorted in place, so a record replays once.
func (rec *treeRecord) walkEntries(root string) (entries []pythonWalkEntry, ok bool) {
	if rec == nil || rec.failed || root != rec.root {
		return nil, false
	}
	top := rec.dirs[root]
	if top == nil {
		return nil, false
	}
	n := 1
	for _, ls := range rec.dirs {
		n += len(ls.entries)
	}
	entries = make([]pythonWalkEntry, 0, n)
	entries = append(entries, pythonWalkEntry{path: root, d: &treeEntry{path: root, name: filepath.Base(root), mode: fs.ModeDir}})
	var visit func(ls *treeListing) bool
	visit = func(ls *treeListing) bool {
		sortTreeEntries(ls.entries)
		for i := range ls.entries {
			e := &ls.entries[i]
			entries = append(entries, pythonWalkEntry{path: e.path, d: e})
			if e.mode.IsDir() {
				sub := rec.dirs[e.path]
				if sub == nil || !visit(sub) {
					return false
				}
			}
		}
		return true
	}
	if !visit(top) {
		return nil, false
	}
	return entries, true
}

// sortTreeEntries orders entries by name as os.ReadDir does.
func sortTreeEntries(es []treeEntry) {
	slices.SortFunc(es, func(a, b treeEntry) int { return strings.Compare(a.name, b.name) })
}

func (e *treeEntry) Name() string               { return e.name }
func (e *treeEntry) IsDir() bool                { return e.mode.IsDir() }
func (e *treeEntry) Type() fs.FileMode          { return e.mode }
func (e *treeEntry) Info() (fs.FileInfo, error) { return treeEntryInfo{e}, nil }

// treeEntryInfo is the lstat view of a recorded entry: its name, type and
// size.
type treeEntryInfo struct{ e *treeEntry }

func (i treeEntryInfo) Name() string       { return i.e.name }
func (i treeEntryInfo) Size() int64        { return i.e.size }
func (i treeEntryInfo) Mode() fs.FileMode  { return i.e.mode }
func (i treeEntryInfo) ModTime() time.Time { return time.Time{} }
func (i treeEntryInfo) IsDir() bool        { return i.e.mode.IsDir() }
func (i treeEntryInfo) Sys() any           { return nil }
