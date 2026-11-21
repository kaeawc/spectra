package storagestate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

func writeSized(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func lstatBytes(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return diskBytes(fi)
}

// sumRegular independently sums Lstat sizes of every non-directory entry
// under dir, without hardlink dedupe.
func sumRegular(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			return nil
		}
		total += diskBytes(fi)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return total
}

func TestWalkTreeTotalsAndSubtree(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "a.bin"), 10_000)
	writeSized(t, filepath.Join(root, "x", "y", "z", "deep.bin"), 70_000)
	for i := range 40 { // wide dir to exercise the worker pool
		writeSized(t, filepath.Join(root, "wide", "d"+string(rune('a'+i%26))+string(rune('a'+i/26)), "f.bin"), 5_000)
	}
	writeSized(t, filepath.Join(root, "Caches", "c1.bin"), 20_000)
	writeSized(t, filepath.Join(root, "Caches", "nested", "c2.bin"), 30_000)

	u := walkTree(root, filepath.Join(root, "Caches"))
	if want := sumRegular(t, root); u.Total != want {
		t.Errorf("Total = %d, want %d", u.Total, want)
	}
	if want := sumRegular(t, filepath.Join(root, "Caches")); u.Tracked[0] != want || want == 0 {
		t.Errorf("Tracked[0] = %d, want %d (non-zero)", u.Tracked[0], want)
	}
	if u.SkippedDirs != 0 {
		t.Errorf("SkippedDirs = %d, want 0", u.SkippedDirs)
	}
}

func TestWalkTreeDoesNotFollowSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSized(t, filepath.Join(outside, "big.bin"), 200_000)
	writeSized(t, filepath.Join(root, "small.bin"), 4_000)
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	u := walkTree(root)
	want := lstatBytes(t, filepath.Join(root, "small.bin")) + lstatBytes(t, link)
	if u.Total != want {
		t.Errorf("Total = %d, want %d (symlink target must not be walked)", u.Total, want)
	}
}

func TestWalkTreeCountsHardlinksOnce(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("inode identity needs syscall.Stat_t")
	}
	root := t.TempDir()
	orig := filepath.Join(root, "a", "orig.bin")
	writeSized(t, orig, 100_000)
	if err := os.MkdirAll(filepath.Join(root, "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(orig, filepath.Join(root, "b", "link.bin")); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}
	u := walkTree(root)
	if want := lstatBytes(t, orig); u.Total != want {
		t.Errorf("Total = %d, want %d (hardlink counted once)", u.Total, want)
	}
}

func TestWalkTreeSkipsUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "ok.bin"), 8_000)
	locked := filepath.Join(root, "locked")
	writeSized(t, filepath.Join(locked, "hidden.bin"), 50_000)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	u := walkTree(root)
	if want := lstatBytes(t, filepath.Join(root, "ok.bin")); u.Total != want {
		t.Errorf("Total = %d, want %d", u.Total, want)
	}
	if u.SkippedDirs != 1 {
		t.Errorf("SkippedDirs = %d, want 1", u.SkippedDirs)
	}
}

func TestWalkTreeSparseFileUsesBlocks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("block accounting needs syscall.Stat_t")
	}
	root := t.TempDir()
	path := filepath.Join(root, "sparse.img")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const apparent = 1 << 30
	if err := f.Truncate(apparent); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	u := walkTree(root)
	if u.Total >= apparent {
		t.Errorf("Total = %d, want sparse allocation well below %d", u.Total, apparent)
	}
	if want := lstatBytes(t, path); u.Total != want {
		t.Errorf("Total = %d, want %d", u.Total, want)
	}
}

func TestWalkTreeMissingRoot(t *testing.T) {
	if u := walkTree(filepath.Join(t.TempDir(), "nope")); u.Total != 0 || len(u.Top) != 0 {
		t.Errorf("walkTree(missing) = %+v, want zero", u)
	}
}

func TestWalkTreeTopBreakdownSumsToTotal(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "loose.bin"), 4_000)
	writeSized(t, filepath.Join(root, "A", "deep", "a.bin"), 30_000)
	writeSized(t, filepath.Join(root, "B", "b.bin"), 60_000)
	u := walkTree(root)
	var sum int64
	for _, b := range u.Top {
		sum += b
	}
	if sum != u.Total {
		t.Errorf("sum(Top) = %d, want Total %d", sum, u.Total)
	}
	if want := sumRegular(t, filepath.Join(root, "A")); u.Top["A"] != want {
		t.Errorf("Top[A] = %d, want %d", u.Top["A"], want)
	}
	if want := lstatBytes(t, filepath.Join(root, "loose.bin")); u.Top[""] != want {
		t.Errorf("Top[\"\"] = %d, want %d", u.Top[""], want)
	}
}

// An inode linked both inside and outside a tracked subtree counts toward
// the subtree regardless of which link the walk reaches first.
func TestWalkTreeTrackedDedupesWithinSubtreeOnly(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("inode identity needs syscall.Stat_t")
	}
	root := t.TempDir()
	inside := filepath.Join(root, "Caches", "x", "orig.bin")
	writeSized(t, inside, 100_000)
	if err := os.Link(inside, filepath.Join(root, "Caches", "dup.bin")); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}
	for _, d := range []string{"a", "b", "c", "d"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(inside, filepath.Join(root, d, "out.bin")); err != nil {
			t.Fatal(err)
		}
	}
	want := lstatBytes(t, inside)
	for range 20 {
		u := walkTree(root, filepath.Join(root, "Caches"), filepath.Join(root, "missing"))
		if u.Total != want || u.Tracked[0] != want || u.Tracked[1] != 0 {
			t.Fatalf("Total=%d Tracked=%v, want %d/[%d 0]", u.Total, u.Tracked, want, want)
		}
	}
}

func TestCollectFootprintBreakdownAndReclaimable(t *testing.T) {
	home := t.TempDir()
	lib := filepath.Join(home, "Library")
	writeSized(t, filepath.Join(lib, "Developer", "Xcode", "DerivedData", "App-abc", "x.o"), 90_000)
	writeSized(t, filepath.Join(lib, "Developer", "CoreSimulator", "Devices", "d.img"), 50_000)
	writeSized(t, filepath.Join(lib, "Caches", "c.bin"), 20_000)
	writeSized(t, filepath.Join(lib, "Mail", "m.emlx"), 10_000)

	fp := CollectFootprint(hostos.Darwin, home, nil)
	if len(fp.Top) == 0 || fp.Top[0].Name != "Developer" {
		t.Fatalf("Top = %+v, want Developer first", fp.Top)
	}
	var sum int64
	for _, d := range fp.Top {
		sum += d.Bytes
	}
	if sum != fp.LibraryBytes {
		t.Errorf("sum(Top) = %d, want LibraryBytes %d", sum, fp.LibraryBytes)
	}
	got := map[string]int64{}
	for _, r := range fp.Reclaimable {
		got[r.Name] = r.Bytes
	}
	if want := sumRegular(t, filepath.Join(lib, "Developer", "Xcode", "DerivedData")); got[AreaDerivedData] != want {
		t.Errorf("DerivedData = %d, want %d", got[AreaDerivedData], want)
	}
	if want := sumRegular(t, filepath.Join(lib, "Developer", "CoreSimulator")); got[AreaCoreSimulator] != want {
		t.Errorf("CoreSimulator = %d, want %d", got[AreaCoreSimulator], want)
	}
	if _, ok := got[AreaIOSDeviceSupport]; ok {
		t.Error("absent area should be omitted")
	}
}

func TestTopDirsRollsUpTail(t *testing.T) {
	in := map[string]int64{"": 5, "a": 100, "b": 90, "c": 80, "d": 1}
	got := topDirs(in, 2)
	want := []DirSize{{"a", 100}, {"b", 90}, {OtherDirName, 86}}
	if len(got) != len(want) {
		t.Fatalf("topDirs = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("topDirs[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestFootprintRootLinuxHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	if got := FootprintRoot(hostos.Linux, "/home/u"); got != "/xdg/cache" {
		t.Errorf("FootprintRoot = %q", got)
	}
	t.Setenv("XDG_CACHE_HOME", "")
	if got := FootprintRoot(hostos.Linux, "/home/u"); got != "/home/u/.cache" {
		t.Errorf("FootprintRoot = %q", got)
	}
}

func TestCollectFootprintDarwinSingleWalk(t *testing.T) {
	home := t.TempDir()
	writeSized(t, filepath.Join(home, "Library", "Preferences", "p.plist"), 12_000)
	writeSized(t, filepath.Join(home, "Library", "Caches", "com.x", "blob"), 40_000)
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

	fp := CollectFootprint(hostos.Darwin, home, func() time.Time { return at })
	if want := sumRegular(t, filepath.Join(home, "Library")); fp.LibraryBytes != want {
		t.Errorf("LibraryBytes = %d, want %d", fp.LibraryBytes, want)
	}
	if want := sumRegular(t, filepath.Join(home, "Library", "Caches")); fp.CachesBytes != want {
		t.Errorf("CachesBytes = %d, want %d", fp.CachesBytes, want)
	}
	if !fp.AsOf.Equal(at) {
		t.Errorf("AsOf = %v, want %v", fp.AsOf, at)
	}
}

func TestCollectSkipUserFootprint(t *testing.T) {
	home := t.TempDir()
	writeSized(t, filepath.Join(home, "Library", "a.bin"), 12_000)
	s := Collect(CollectOptions{
		Home:              home,
		OS:                hostos.Darwin,
		CmdRunner:         func(string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		SkipUserFootprint: true,
	})
	if s.UserLibraryBytes != 0 || !s.UserFootprintAsOf.IsZero() {
		t.Errorf("footprint collected despite SkipUserFootprint: %+v", s)
	}
}
