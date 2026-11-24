package detect

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type walkView struct {
	Path  string
	Name  string
	IsDir bool
	Type  fs.FileMode
	Size  int64 // Info().Size() for regular files
}

func viewOf(t *testing.T, path string, d fs.DirEntry) walkView {
	t.Helper()
	v := walkView{Path: path, Name: d.Name(), IsDir: d.IsDir(), Type: d.Type()}
	if d.Type().IsRegular() {
		fi, err := d.Info()
		if err != nil {
			t.Fatalf("Info(%s): %v", path, err)
		}
		v.Size = fi.Size()
	}
	return v
}

// realWalk is what pythonScan.walk recorded before the replay existed.
func realWalk(t *testing.T, root string) (views []walkView, errs int) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil {
			views = append(views, viewOf(t, path, d))
		} else {
			errs++
		}
		return nil
	})
	return views, errs
}

func replayedWalk(t *testing.T, app string, lstatOnly bool) ([]walkView, bool) {
	t.Helper()
	rec := newTreeRecord(filepath.Join(app, "Contents"))
	bundleTreeStatsRec(lstatOnly, app, rec)
	entries, ok := rec.walkEntries(filepath.Join(app, "Contents"))
	var views []walkView
	for _, e := range entries {
		if e.err != nil {
			t.Fatalf("replayed entry %s carries an error", e.path)
		}
		views = append(views, viewOf(t, e.path, e.d))
	}
	return views, ok
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestTreeRecordReplaysWalkDir pins that the recorded Contents listing
// replays exactly the callbacks filepath.WalkDir makes: sorted pre-order,
// directory entries included, symlinks unfollowed, the same types and sizes.
func TestTreeRecordReplaysWalkDir(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Py.app")
	writeTree(t, app, map[string]string{
		"Contents/Info.plist":                                          "<plist/>",
		"Contents/MacOS/launcher":                                      "#!/bin/sh\nexec python3\n",
		"Contents/Resources/a-b":                                       "x",
		"Contents/Resources/a/b":                                       "yy",
		"Contents/Resources/B":                                         "upper sorts first",
		"Contents/Resources/lib/python3.11/site.py":                    "import os\n",
		"Contents/Resources/lib/python3.11/pkg-1.0.dist-info/METADATA": "Name: pkg\nVersion: 1.0\n",
		"Contents/Frameworks/x.so":                                     string(make([]byte, 5000)),
		"Outside/ignored":                                              "not under Contents",
	})
	for _, dir := range []string{"Contents/Empty", "Contents/Resources/empty-dir"} {
		if err := os.MkdirAll(filepath.Join(app, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("lib/python3.11", filepath.Join(app, "Contents/Resources/current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent", filepath.Join(app, "Contents/dangling")); err != nil {
		t.Fatal(err)
	}
	want, errs := realWalk(t, filepath.Join(app, "Contents"))
	if errs != 0 {
		t.Fatalf("fixture walk had %d errors", errs)
	}
	for _, lstatOnly := range []bool{false, true} {
		got, ok := replayedWalk(t, app, lstatOnly)
		if !ok {
			t.Fatalf("lstatOnly=%v: complete walk not replayable", lstatOnly)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("lstatOnly=%v replay:\n%+v\nwant WalkDir:\n%+v", lstatOnly, got, want)
		}
	}
	// A trailing slash on the bundle path yields the same paths.
	got, ok := replayedWalk(t, app+"/", false)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("trailing slash: ok=%v replay differs", ok)
	}
}

func TestTreeRecordNotReplayable(t *testing.T) {
	t.Run("unreadable directory", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads mode-000 directories")
		}
		app := filepath.Join(t.TempDir(), "Locked.app")
		writeTree(t, app, map[string]string{"Contents/Locked/f": "x", "Contents/MacOS/m": "y"})
		locked := filepath.Join(app, "Contents/Locked")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		for _, lstatOnly := range []bool{false, true} {
			if _, ok := replayedWalk(t, app, lstatOnly); ok {
				t.Fatalf("lstatOnly=%v: walk with an unreadable directory must fall back to WalkDir", lstatOnly)
			}
		}
	})
	t.Run("no Contents", func(t *testing.T) {
		app := filepath.Join(t.TempDir(), "Bare.app")
		writeTree(t, app, map[string]string{"other": "x"})
		if _, ok := replayedWalk(t, app, false); ok {
			t.Fatal("missing Contents must not replay")
		}
	})
	t.Run("symlinked Contents", func(t *testing.T) {
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"real/MacOS/m": "x"})
		app := filepath.Join(dir, "Link.app")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(app, "Contents")); err != nil {
			t.Fatal(err)
		}
		if _, ok := replayedWalk(t, app, false); ok {
			t.Fatal("symlinked Contents must not replay")
		}
	})
	t.Run("other root", func(t *testing.T) {
		app := filepath.Join(t.TempDir(), "A.app")
		writeTree(t, app, map[string]string{"Contents/MacOS/m": "x"})
		rec := newTreeRecord(filepath.Join(app, "Contents"))
		bundleTreeStatsRec(false, app, rec)
		if _, ok := rec.walkEntries(filepath.Join(app, "Contents", "MacOS")); ok {
			t.Fatal("only the recorded root replays")
		}
		var nilRec *treeRecord
		if _, ok := nilRec.walkEntries(app); ok {
			t.Fatal("nil record replayed")
		}
	})
}

// TestPythonScanReplayMatchesWalk pins the scan result with and without the
// recorded listing on a py2app-shaped bundle.
func TestPythonScanReplayMatchesWalk(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Py.app")
	writeTree(t, app, map[string]string{
		"Contents/MacOS/Py":                      "#!/bin/sh\nPYTHONPATH=x exec /opt/homebrew/bin/python3.12 \"$@\"\n",
		"Contents/Resources/__boot__.py":         "boot",
		"Contents/Resources/lib/python3.12/a.py": "x",
		"Contents/Resources/lib/python3.12/site-packages/req-2.0.dist-info/METADATA": "Name: req\nVersion: 2.0\n",
		"Contents/Resources/lib/python3.12/lib-dynload/_ssl.so":                      "not a real macho",
	})
	runner := &fakePythonCommandRunner{}
	want := pythonAppScanner{fs: osPythonAppFS{}, commands: runner}.Scan(app)
	_, rec := bundleTreeStatsRecorded(app)
	got := pythonAppScanner{fs: osPythonAppFS{}, commands: runner, tree: rec}.Scan(app)
	if want == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed scan = %+v, want %+v", got, want)
	}
}
