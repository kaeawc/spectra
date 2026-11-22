package detect

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// refTreeStats is the original filepath.WalkDir implementation, kept as the
// oracle the single-pass walker must match.
func refTreeStats(appPath string) (actual, apparent int64, jars int, jvmDir string) {
	contents := filepath.Join(appPath, "Contents")
	_ = filepath.WalkDir(appPath, func(path string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".jar") && strings.HasPrefix(path, contents) {
			jars++
		}
		fi, _ := d.Info()
		if fi == nil || !fi.Mode().IsRegular() {
			return nil
		}
		actual += diskBytes(fi)
		apparent += fi.Size()
		return nil
	})
	_ = filepath.WalkDir(contents, func(path string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() {
			return nil
		}
		if d.Name() == "libjvm.dylib" {
			jvmDir = filepath.Dir(path)
			return io.EOF
		}
		return nil
	})
	return
}

func TestBundleTreeStatsMatchesWalkDir(t *testing.T) {
	app := makeBundle(t, "Tree")
	files := map[string]string{
		"Contents/Resources/a.jar":                       "jar",
		"Contents/Resources/java/lib/b.jar":              "jarjar",
		"Contents/Frameworks/x-y/libjvm.dylib":           "z",
		"Contents/Frameworks/x/lib/libjvm.dylib":         "zz",
		"Contents/Frameworks/x/sub/deeper/file.bin":      strings.Repeat("d", 9000),
		"Contents/Resources/zeta/libjvm.dylib":           "late",
		"Contents/Resources/empty-dir-sibling/keep.json": "{}",
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
	// A symlink (to a file and to a directory) must not be followed or sized.
	if err := os.Symlink(filepath.Join(app, "Contents", "Resources", "a.jar"), filepath.Join(app, "Contents", "Resources", "link.jar")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(app, "Contents", "Frameworks"), filepath.Join(app, "Contents", "Resources", "fwlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app, "Contents", "Frameworks", "unreadable"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := bundleTreeStats(app)
	actual, apparent, jars, jvmDir := refTreeStats(app)
	if got.actual != actual || got.apparent != apparent || got.jars != jars || got.libjvmDir != jvmDir {
		t.Fatalf("bundleTreeStats = %+v; WalkDir oracle = actual %d apparent %d jars %d jvm %q",
			got, actual, apparent, jars, jvmDir)
	}
	if got.libjvmDir == "" {
		t.Fatal("expected a libjvm directory")
	}
	if n := treeBytes(app); n != actual {
		t.Fatalf("treeBytes = %d, want %d", n, actual)
	}
}

func TestBundleTreeStatsSymlinkedBundleRoot(t *testing.T) {
	target := makeBundle(t, "Real")
	link := filepath.Join(t.TempDir(), "link.app")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want := bundleTreeStats(target)
	if want.actual == 0 {
		t.Fatal("target bundle sized 0")
	}
	if got := bundleTreeStats(link + "/"); got != want {
		t.Fatalf("link.app/ = %+v, want target's %+v", got, want)
	}
	actual, apparent, jars, jvm := refTreeStats(link)
	if got := bundleTreeStats(link); got.actual != actual || got.apparent != apparent || got.jars != jars || got.libjvmDir != jvm {
		t.Fatalf("link.app = %+v, WalkDir gives %d/%d/%d/%q", got, actual, apparent, jars, jvm)
	}
	if n := treeBytes(link + "/"); n != want.actual {
		t.Fatalf("treeBytes(link.app/) = %d, want %d", n, want.actual)
	}
}

func TestBundleTreeStatsNoJVM(t *testing.T) {
	app := makeBundle(t, "NoJVM")
	if got := bundleTreeStats(app); got.libjvmDir != "" || got.jars != 0 || got.actual == 0 {
		t.Fatalf("bundleTreeStats = %+v", got)
	}
	if treeBytes(filepath.Join(app, "missing")) != 0 {
		t.Fatal("missing root should size to 0")
	}
}

func TestPathBefore(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"a/b/c", "a-b/c", true}, // component-wise: "a" < "a-b"
		{"a/z", "a/b/c", false},
		{"a/b", "a/b/c", true},
		{"a/b/c", "a/b", false},
		{"a/b", "a/b", false},
	}
	for _, c := range cases {
		if got := pathBefore(c.a, c.b); got != c.want {
			t.Errorf("pathBefore(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}
