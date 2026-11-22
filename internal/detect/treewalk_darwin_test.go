//go:build darwin

package detect

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

type visited struct {
	regular          bool
	actual, apparent int64
	count            int
}

// walkVisits records every visit of one walk, keyed by path.
func walkVisits(lstatOnly bool, root string) map[string]visited {
	got := map[string]visited{}
	walkNonDirsWith(lstatOnly, root, func(path, name string, regular bool, actual, apparent int64) {
		if !strings.HasSuffix(path, name) {
			panic("visit name " + name + " is not the tail of " + path)
		}
		v := got[path]
		v.regular, v.actual, v.apparent = regular, actual, apparent
		v.count++
		got[path] = v
	})
	return got
}

// parityTree builds a bundle exercising every accounting rule the bulk
// reader must keep: symlinks to files and directories (not followed, not
// sized as regular files), hard links (each link counted), a sparse file
// (allocated blocks differ from logical size), nested jars, libjvm in
// several places, a FIFO, and a directory wide enough for several
// getattrlistbulk batches.
func parityTree(t *testing.T, parent string) string {
	t.Helper()
	app := filepath.Join(parent, "Parity.app")
	files := map[string]string{
		"Contents/Info.plist":                       "<plist/>",
		"Contents/MacOS/main":                       strings.Repeat("m", 70000),
		"Contents/Resources/a.jar":                  "jar",
		"Contents/Resources/java/lib/b.jar":         "jarjar",
		"Contents/Resources/java/lib/deep/c.jar":    "",
		"Contents/Frameworks/x-y/libjvm.dylib":      "z",
		"Contents/Frameworks/x/lib/libjvm.dylib":    "zz",
		"Contents/Frameworks/x/sub/deeper/file.bin": strings.Repeat("d", 9000),
		"Contents/Resources/zeta/libjvm.dylib":      "late",
		"Contents/Resources/empty.dat":              "",
		"Outside.jar":                               "not under Contents",
	}
	for rel, body := range files {
		p := filepath.Join(app, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := filepath.Join(app, "Contents", "Resources")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(res, "a.jar"), filepath.Join(res, "link.jar")))
	must(os.Symlink(filepath.Join(app, "Contents", "Frameworks"), filepath.Join(res, "fwlink")))
	must(os.Symlink("dangling-target", filepath.Join(res, "dangling")))
	must(os.Symlink(filepath.Join(app, "Contents", "Frameworks", "x", "lib", "libjvm.dylib"), filepath.Join(res, "libjvm.dylib")))
	must(os.Link(filepath.Join(app, "Contents", "MacOS", "main"), filepath.Join(res, "main-hardlink")))
	must(os.Link(filepath.Join(app, "Contents", "MacOS", "main"), filepath.Join(app, "Contents", "main-hardlink2")))
	must(syscall.Mkfifo(filepath.Join(res, "fifo"), 0o644))

	sparse, err := os.Create(filepath.Join(res, "sparse.img"))
	must(err)
	must(sparse.Truncate(64 << 20))
	_, err = sparse.WriteAt([]byte("tail"), 64<<20-4)
	must(err)
	must(sparse.Close())

	wide := filepath.Join(res, "wide")
	must(os.MkdirAll(wide, 0o755))
	for i := range 3000 {
		must(os.WriteFile(filepath.Join(wide, "entry-with-a-longish-name-"+strconv.Itoa(i)+".dat"), []byte("x"), 0o644))
	}
	return app
}

func assertWalkParity(t *testing.T, root string) {
	t.Helper()
	want := walkVisits(true, root)
	got := walkVisits(false, root)
	if len(got) != len(want) {
		t.Errorf("%s: bulk visited %d entries, lstat %d", root, len(got), len(want))
	}
	for p, w := range want {
		g, ok := got[p]
		switch {
		case !ok:
			t.Errorf("%s: bulk walk missed %s", root, p)
		case g.count != 1:
			t.Errorf("%s: bulk walk visited %s %d times", root, p, g.count)
		case g.regular != w.regular:
			t.Errorf("%s: %s regular bulk=%v lstat=%v", root, p, g.regular, w.regular)
		case w.regular && (g.actual != w.actual || g.apparent != w.apparent):
			t.Errorf("%s: %s sizes bulk=%d/%d lstat=%d/%d", root, p, g.actual, g.apparent, w.actual, w.apparent)
		}
	}
	if g, w := bundleTreeStatsWith(false, root), bundleTreeStatsWith(true, root); g != w {
		t.Errorf("%s: bundleTreeStats bulk=%+v lstat=%+v", root, g, w)
	}
	if g, w := treeBytesWith(false, root), treeBytesWith(true, root); g != w {
		t.Errorf("%s: treeBytes bulk=%d lstat=%d", root, g, w)
	}
}

// TestWalkNonDirsBulkMatchesLstat compares the getattrlistbulk walk with the
// lstat walk entry by entry, under the default TMPDIR (a symlinked
// /var/folders path on macOS) and under /tmp (itself a symlink).
func TestWalkNonDirsBulkMatchesLstat(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "spectra-treewalk-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	for name, parent := range map[string]string{"TMPDIR": t.TempDir(), "/tmp": tmp} {
		t.Run(name, func(t *testing.T) {
			app := parityTree(t, parent)
			link := filepath.Join(parent, "Link.app")
			if err := os.Symlink(app, link); err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{
				app, app + "/", link, link + "/",
				filepath.Join(app, "Contents", "MacOS", "main"),
				filepath.Join(app, "missing"),
			} {
				assertWalkParity(t, root)
			}

			st := bundleTreeStats(app)
			if st.jars != 4 { // a, b, c and the link.jar symlink; not Outside.jar
				t.Errorf("jars = %d, want 4", st.jars)
			}
			if want := filepath.Join(app, "Contents", "Frameworks", "x", "lib"); st.libjvmDir != want { // component-wise "x" < "x-y"
				t.Errorf("libjvmDir = %q, want %q", st.libjvmDir, want)
			}
			if st.actual >= st.apparent {
				t.Errorf("sparse file should make actual (%d) < apparent (%d)", st.actual, st.apparent)
			}
			// The executable's two hard links are each sized in full.
			n := treeBytes(filepath.Join(app, "Contents", "MacOS", "main"))
			visits := walkVisits(false, app)
			for _, rel := range []string{"Contents/Resources/main-hardlink", "Contents/main-hardlink2"} {
				if v := visits[filepath.Join(app, rel)]; !v.regular || v.actual != n || n == 0 {
					t.Errorf("hard link %s = %+v, want regular with %d bytes", rel, v, n)
				}
			}
			for _, rel := range []string{"Contents/Resources/link.jar", "Contents/Resources/fwlink", "Contents/Resources/fifo"} {
				if v := visits[filepath.Join(app, rel)]; v.count != 1 || v.regular {
					t.Errorf("%s = %+v, want one non-regular visit", rel, v)
				}
			}
			want := st
			want.libjvmDir = link + strings.TrimPrefix(st.libjvmDir, app)
			if got := bundleTreeStats(link + "/"); got != want {
				t.Errorf("Link.app/ = %+v, want %+v", got, want)
			}
			if got := bundleTreeStats(link); got.actual != 0 || got.jars != 0 {
				t.Errorf("Link.app (unfollowed symlink root) = %+v", got)
			}
		})
	}
}

// TestWalkNonDirsBulkMatchesLstatSystemApp checks parity on a real
// bundle, whose files are compressed (decmpfs) on a sealed system volume, so
// allocated and logical sizes come from the compression layer.
func TestWalkNonDirsBulkMatchesLstatSystemApp(t *testing.T) {
	for _, app := range []string{"/System/Applications/Calculator.app", "/System/Applications/TextEdit.app"} {
		if _, err := os.Stat(app); err != nil {
			t.Skipf("%s not present", app)
		}
		assertWalkParity(t, app)
	}
}

// BenchmarkTreeWalk compares the bulk and lstat walks over a wide, nested
// synthetic bundle.
func BenchmarkTreeWalk(b *testing.B) {
	app := makeBundle(b, "Walk")
	for d := range 20 {
		dir := filepath.Join(app, "Contents", "Resources", "d"+strconv.Itoa(d))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		for i := range 250 {
			if err := os.WriteFile(filepath.Join(dir, "f"+strconv.Itoa(i)+".dat"), []byte("data"), 0o644); err != nil {
				b.Fatal(err)
			}
		}
	}
	for _, mode := range []struct {
		name      string
		lstatOnly bool
	}{{"bulk", false}, {"lstat", true}} {
		b.Run(mode.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if bundleTreeStatsWith(mode.lstatOnly, app).actual == 0 {
					b.Fatal("sized 0")
				}
			}
		})
	}
}
