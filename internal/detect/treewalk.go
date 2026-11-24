package detect

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// readDirBatch bounds how many entries one ReadDir call materializes.
const readDirBatch = 256

// walkNonDirsWith visits every non-directory entry under root (or root itself
// when it is not a directory) with its lstat-derived sizes. Symlinks are not
// followed. Unlike filepath.WalkDir it neither sorts directory entries nor
// allocates an os.FileInfo per file: it lstats into one reused buffer.
// Unreadable directories and vanished entries are skipped. The visit order
// is unspecified. On Darwin whole directories are listed with
// getattrlistbulk; lstatOnly forces one lstat per entry instead, so tests
// can compare the two.
func walkNonDirsWith(lstatOnly bool, root string, visit treeVisit) {
	walkTree(lstatOnly, root, visit, nil)
}

// walkTree is walkNonDirsWith that also records into rec every directory
// listing under rec's root. A nil rec records nothing.
func walkTree(lstatOnly bool, root string, visit treeVisit, rec *treeRecord) {
	var st statBuf
	fi, err := os.Lstat(root)
	if err != nil {
		return
	}
	if !fi.IsDir() {
		visit(root, fi.Name(), fi.Mode().IsRegular(), diskBytes(fi), fi.Size())
		return
	}
	// The root is lstat'd as given (a trailing slash follows a symlinked
	// bundle, as filepath.WalkDir does); children use the cleaned path.
	stack := []string{filepath.Clean(root)}
	bulk := newTreeBulk(lstatOnly)
	defer bulk.release()
	for len(stack) > 0 {
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ls := rec.listing(dir)
		var ok bool
		if stack, ok = bulk.readDir(dir, stack, &st, visit, ls); !ok {
			stack = readTreeDir(dir, stack, &st, visit, ls)
		}
	}
}

func readTreeDir(dir string, stack []string, st *statBuf, visit treeVisit, ls *treeListing) []string {
	f, err := os.Open(dir)
	if err != nil {
		ls.fail()
		return stack
	}
	defer f.Close()
	for {
		ents, err := f.ReadDir(readDirBatch)
		for _, e := range ents {
			p := dir + "/" + e.Name()
			if e.IsDir() {
				ls.add(p, len(dir), fs.ModeDir, 0)
				stack = append(stack, p)
				continue
			}
			regular, actual, apparent, ok := lstatRegular(p, st)
			if !ok {
				// Matches WalkDir + Info(): the entry is visited but
				// contributes no size.
				ls.fail()
				visit(p, e.Name(), false, 0, 0)
				continue
			}
			// e.Type() is the type filepath.WalkDir's DirEntry reports.
			ls.add(p, len(dir), e.Type(), apparent)
			visit(p, e.Name(), regular, actual, apparent)
		}
		if err != nil {
			if err != io.EOF {
				ls.fail()
			}
			return stack
		}
	}
}

// treeVisit receives one non-directory entry of a tree walk.
type treeVisit func(path, name string, regular bool, actual, apparent int64)

// bundleTree is what one walk of an .app bundle yields.
type bundleTree struct {
	// actual is the sparse-aware disk usage of all regular files; apparent is
	// their logical size. Equal for non-sparse files.
	actual, apparent int64
	// jars counts .jar entries under Contents/.
	jars int
	// libjvmDir is the directory holding the first libjvm.dylib under
	// Contents/ in lexical depth-first order, or "".
	libjvmDir string
}

// bundleTreeStats walks the bundle exactly once for the size totals, the
// number of .jar files and the bundled-JVM location, which classification,
// size accounting, and dependency scanning each used to walk for separately.
func bundleTreeStats(appPath string) bundleTree {
	return bundleTreeStatsWith(false, appPath)
}

// bundleTreeStatsRecorded is bundleTreeStats that also records the listing
// of Contents, so the Python scan can replay it instead of walking again.
func bundleTreeStatsRecorded(appPath string) (bundleTree, *treeRecord) {
	rec := newTreeRecord(filepath.Join(appPath, "Contents"))
	return bundleTreeStatsRec(false, appPath, rec), rec
}

func bundleTreeStatsWith(lstatOnly bool, appPath string) bundleTree {
	return bundleTreeStatsRec(lstatOnly, appPath, nil)
}

func bundleTreeStatsRec(lstatOnly bool, appPath string, rec *treeRecord) bundleTree {
	root := filepath.Clean(appPath)
	contents := root + "/Contents"
	var t bundleTree
	var jvmFile string
	walkTree(lstatOnly, appPath, func(path, name string, regular bool, actual, apparent int64) {
		if strings.HasPrefix(path, contents) {
			if strings.HasSuffix(name, ".jar") {
				t.jars++
			}
			if name == "libjvm.dylib" && strings.HasPrefix(path, contents+"/") &&
				(jvmFile == "" || pathBefore(path, jvmFile)) {
				jvmFile = path
			}
		}
		if regular {
			t.actual += actual
			t.apparent += apparent
		}
	}, rec)
	if jvmFile != "" {
		t.libjvmDir = jvmFile[:strings.LastIndexByte(jvmFile, '/')]
	}
	return t
}

// treeBytes returns the actual disk usage of all regular files under root.
func treeBytes(root string) int64 {
	return treeBytesWith(false, root)
}

func treeBytesWith(lstatOnly bool, root string) (actual int64) {
	walkNonDirsWith(lstatOnly, root, func(_, _ string, regular bool, a, _ int64) {
		if regular {
			actual += a
		}
	})
	return actual
}

// pathBefore reports whether a precedes b in a lexical depth-first walk,
// which compares paths component by component (not byte by byte, so
// "a/b" < "a-b" even though '/' sorts after '-').
func pathBefore(a, b string) bool {
	for a != "" && b != "" {
		ac, ar, _ := strings.Cut(a, "/")
		bc, br, _ := strings.Cut(b, "/")
		if ac != bc {
			return ac < bc
		}
		a, b = ar, br
	}
	return a == "" && b != ""
}
