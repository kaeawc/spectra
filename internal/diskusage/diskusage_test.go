package diskusage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func walk(t *testing.T, root string, o Options) *Result {
	t.Helper()
	r, err := Walk(context.Background(), root, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func dirSize(r *Result, path string) (uint64, bool) {
	for _, d := range r.Dirs {
		if d.Path == path {
			return d.AllocatedBytes, true
		}
	}
	return 0, false
}

func TestCumulativeDirSizes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f1"), 64*1024)
	writeFile(t, filepath.Join(root, "a", "b", "f2"), 128*1024)
	r := walk(t, root, Options{})
	a, okA := dirSize(r, filepath.Join(root, "a"))
	b, okB := dirSize(r, filepath.Join(root, "a", "b"))
	if !okA || !okB {
		t.Fatalf("dirs missing: %+v", r.Dirs)
	}
	if a <= b || b < 128*1024 {
		t.Errorf("a=%d b=%d, want a > b >= 128K", a, b)
	}
	if r.TotalBytes < a {
		t.Errorf("total %d < a %d", r.TotalBytes, a)
	}
	if r.FileCount != 2 || r.DirCount != 3 {
		t.Errorf("files=%d dirs=%d, want 2 and 3", r.FileCount, r.DirCount)
	}
}

func TestTopNOrderingAndTruncation(t *testing.T) {
	root := t.TempDir()
	for i, sz := range []int{40, 400, 4, 4000, 40000} {
		writeFile(t, filepath.Join(root, string(rune('a'+i))), sz*1024)
	}
	r := walk(t, root, Options{Top: 3})
	if len(r.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(r.Files))
	}
	for i := 1; i < len(r.Files); i++ {
		if r.Files[i-1].AllocatedBytes < r.Files[i].AllocatedBytes {
			t.Errorf("not descending: %+v", r.Files)
		}
	}
	if filepath.Base(r.Files[0].Path) != "e" || filepath.Base(r.Files[2].Path) != "b" {
		t.Errorf("unexpected top files: %+v", r.Files)
	}
}

func TestDepthLimitsReportingNotTotals(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "b", "c", "f"), 256*1024)
	deep := walk(t, root, Options{})
	shallow := walk(t, root, Options{Depth: 1})
	if len(shallow.Dirs) != 1 || filepath.Base(shallow.Dirs[0].Path) != "a" {
		t.Errorf("depth 1 dirs = %+v", shallow.Dirs)
	}
	if shallow.TotalBytes != deep.TotalBytes {
		t.Errorf("totals differ: %d vs %d", shallow.TotalBytes, deep.TotalBytes)
	}
	a1, ok1 := dirSize(shallow, filepath.Join(root, "a"))
	a2, ok2 := dirSize(deep, filepath.Join(root, "a"))
	if !ok1 || !ok2 || a1 == 0 {
		t.Fatalf("a not found: shallow=%v deep=%v size=%d", ok1, ok2, a1)
	}
	if a1 != a2 {
		t.Errorf("a size differs: %d vs %d", a1, a2)
	}
}

func TestHardLinkCountedOnce(t *testing.T) {
	root := t.TempDir()
	orig := filepath.Join(root, "orig")
	writeFile(t, orig, 128*1024)
	if err := os.Link(orig, filepath.Join(root, "link")); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	r := walk(t, root, Options{})
	if r.FileCount != 1 {
		t.Errorf("file count = %d, want 1", r.FileCount)
	}
	if len(r.Files) != 1 {
		t.Errorf("files = %+v, want one entry", r.Files)
	}
}

func TestSymlinkNotFollowed(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "big"), 1024*1024)
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	r := walk(t, root, Options{})
	if r.TotalBytes >= 1024*1024 {
		t.Errorf("total %d suggests symlink was followed", r.TotalBytes)
	}
	for _, f := range r.Files {
		if strings.Contains(f.Path, "big") {
			t.Errorf("followed symlink to %s", f.Path)
		}
	}
}

func TestSparseFileAllocatedLessThanApparent(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "sparse")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	r := walk(t, root, Options{})
	if len(r.Files) != 1 {
		t.Fatalf("files = %+v", r.Files)
	}
	fe := r.Files[0]
	if fe.AllocatedBytes >= 64*1024*1024 {
		t.Skipf("filesystem did not make the file sparse (allocated %d)", fe.AllocatedBytes)
	}
	if fe.ApparentBytes != 64*1024*1024 {
		t.Errorf("apparent = %d, want 64M", fe.ApparentBytes)
	}
}

func TestUnreadableDirRecorded(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 is not enforced")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	writeFile(t, filepath.Join(locked, "x"), 1024)
	writeFile(t, filepath.Join(root, "ok", "y"), 128*1024)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	r := walk(t, root, Options{})
	if len(r.Unreadable) != 1 || r.Unreadable[0].Path != locked || r.Unreadable[0].Error == "" {
		t.Errorf("unreadable = %+v", r.Unreadable)
	}
	if _, ok := dirSize(r, filepath.Join(root, "ok")); !ok {
		t.Error("walk did not continue past unreadable dir")
	}
}

func TestMissingPath(t *testing.T) {
	if _, err := Walk(context.Background(), filepath.Join(t.TempDir(), "nope"), Options{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestMinSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "small"), 1024)
	writeFile(t, filepath.Join(root, "big"), 1024*1024)
	r := walk(t, root, Options{MinSize: 512 * 1024})
	if len(r.Files) != 1 || filepath.Base(r.Files[0].Path) != "big" {
		t.Errorf("files = %+v", r.Files)
	}
	if r.FileCount != 2 {
		t.Errorf("file count = %d, want 2 (min-size filters reporting only)", r.FileCount)
	}
}

func TestCancelledContextPartial(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f"), 1024)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := Walk(ctx, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Partial {
		t.Error("expected partial")
	}
}

func TestSymlinkedRootKeepsTypedPrefix(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "d", "f"), 64*1024)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := walk(t, link, Options{})
	if r.Root != link || r.ResolvedRoot == "" {
		t.Errorf("root=%q resolved=%q", r.Root, r.ResolvedRoot)
	}
	if _, ok := dirSize(r, filepath.Join(link, "d")); !ok {
		t.Errorf("entries not under typed root: %+v", r.Dirs)
	}
}

func TestVisitedDirectorySkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f"), 4096)
	writeFile(t, filepath.Join(root, "b", "f"), 4096)
	same := func(fi os.FileInfo) fileStat {
		st := statOf(fi)
		if fi.IsDir() {
			st.dev, st.ino, st.ok = 1, 42, true
		}
		return st
	}
	r, err := walkWith(context.Background(), root, Options{}, same)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skipped) != 2 {
		t.Fatalf("skipped = %+v", r.Skipped)
	}
	for _, s := range r.Skipped {
		if s.Reason != "already visited" {
			t.Errorf("reason = %q", s.Reason)
		}
	}
}

func TestOtherFilesystemRecorded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "m", "f"), 4096)
	other := func(fi os.FileInfo) fileStat {
		st := statOf(fi)
		if fi.IsDir() && fi.Name() == "m" {
			st.dev++
		}
		return st
	}
	r, err := walkWith(context.Background(), root, Options{}, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Reason != "different filesystem" {
		t.Errorf("skipped = %+v", r.Skipped)
	}
	r, _ = walkWith(context.Background(), root, Options{CrossMounts: true}, other)
	if len(r.Skipped) != 0 {
		t.Errorf("cross-mounts skipped = %+v", r.Skipped)
	}
}

func TestCancelMidDirectoryPartial(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 3000; i++ {
		writeFile(t, filepath.Join(root, "d", fmt.Sprintf("f%04d", i)), 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	fn := func(fi os.FileInfo) fileStat {
		if calls++; calls == 50 {
			cancel()
		}
		return statOf(fi)
	}
	r, err := walkWith(ctx, root, Options{}, fn)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Partial || r.FileCount >= 3000 {
		t.Errorf("partial=%v files=%d", r.Partial, r.FileCount)
	}
}

func TestJSONShape(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "f"), 4096)
	b, err := json.Marshal(walk(t, root, Options{}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"root", "total_bytes", "file_count", "dir_count", "top_dirs", "top_files", "unreadable", "partial", "elapsed_ms", "skipped"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q in %s", k, b)
		}
	}
	if m["unreadable"] == nil {
		t.Error("unreadable should be [] not null")
	}
}

func TestDotDotAfterSymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, "real", "data", "f"), 64*1024)
	if err := os.MkdirAll(filepath.Join(base, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real", "sub"), filepath.Join(base, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := walk(t, base+"/link/..", Options{}) // Join would clean the ".."
	if len(r.Unreadable) != 0 || r.FileCount != 1 {
		t.Errorf("files=%d unreadable=%+v", r.FileCount, r.Unreadable)
	}
}

type countingDir struct {
	dirHandle
	open *int
}

func (c countingDir) Close() error { *c.open--; return c.dirHandle.Close() }

func TestDeepTreeBoundedHandles(t *testing.T) {
	root := t.TempDir()
	dir := root
	for i := 0; i < 300; i++ {
		dir = filepath.Join(dir, "d")
	}
	writeFile(t, filepath.Join(dir, "bottom"), 64*1024)
	var open, max int
	openFn := func(p string) (dirHandle, error) {
		h, err := osOpenDir(p)
		if err != nil {
			return nil, err
		}
		if open++; open > max {
			max = open
		}
		return countingDir{h, &open}, nil
	}
	r, err := walkOpen(context.Background(), root, Options{}, statOf, openFn)
	if err != nil {
		t.Fatal(err)
	}
	if r.FileCount != 1 || len(r.Unreadable) != 0 || r.DirCount != 301 {
		t.Errorf("files=%d dirs=%d unreadable=%+v", r.FileCount, r.DirCount, r.Unreadable)
	}
	if max > 2 {
		t.Errorf("max simultaneously open dirs = %d", max)
	}
}
