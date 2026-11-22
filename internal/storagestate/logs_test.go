package storagestate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestCollectLogFiles(t *testing.T) {
	home := t.TempDir()

	// $HOME/Library/Logs/Foo/foo.log
	mustWrite(t, filepath.Join(home, "Library", "Logs", "Foo", "foo.log"), "log")
	// $HOME/Library/Logs/Bar/bar.txt — under /Logs/ so still counts
	mustWrite(t, filepath.Join(home, "Library", "Logs", "Bar", "bar.txt"), "log")
	// $HOME/Library/Application Support/Slack/Logs/main.log — owner=Slack
	mustWrite(t, filepath.Join(home, "Library", "Application Support", "Slack", "Logs", "main.log"), "log")
	// $HOME/Library/Application Support/Slack/Cache/x — NOT under Logs, skip
	mustWrite(t, filepath.Join(home, "Library", "Application Support", "Slack", "Cache", "x.bin"), "data")
	// non-log file under user library — skip
	mustWrite(t, filepath.Join(home, "Library", "Logs", "Foo", "readme.txt"), "txt")

	files := CollectLogFiles(home, hostos.Darwin)

	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Owner
	}

	want := map[string]string{
		filepath.Join(home, "Library", "Logs", "Foo", "foo.log"):                           "",
		filepath.Join(home, "Library", "Logs", "Bar", "bar.txt"):                           "",
		filepath.Join(home, "Library", "Application Support", "Slack", "Logs", "main.log"): "Slack",
	}

	for path, owner := range want {
		if gotOwner, ok := got[path]; !ok {
			t.Errorf("missing %q from results", path)
		} else if gotOwner != owner {
			t.Errorf("%q: owner = %q, want %q", path, gotOwner, owner)
		}
	}
	if got[filepath.Join(home, "Library", "Application Support", "Slack", "Cache", "x.bin")] != "" {
		// the empty-string default could mask a false positive; assert the path simply isn't present
		for _, f := range files {
			if f.Path == filepath.Join(home, "Library", "Application Support", "Slack", "Cache", "x.bin") {
				t.Errorf("non-log file should not appear in results")
			}
		}
	}
}

func TestCollectLogFiles_SortedBySizeDesc(t *testing.T) {
	home := t.TempDir()
	mustWrite(t, filepath.Join(home, "Library", "Logs", "small.log"), "x")
	mustWrite(t, filepath.Join(home, "Library", "Logs", "big.log"), "xxxxxxxxxxxxxxxxxxxx")

	files := CollectLogFiles(home, hostos.Darwin)
	if len(files) < 2 {
		t.Fatalf("expected ≥2 files, got %d", len(files))
	}
	if files[0].SizeBytes < files[1].SizeBytes {
		t.Errorf("results should be sorted size-desc, got %d before %d", files[0].SizeBytes, files[1].SizeBytes)
	}
}

func TestIsLogShapedFile(t *testing.T) {
	cases := map[string]bool{
		"/Users/foo/Library/Logs/x.log":             true,
		"/Users/foo/Library/Logs/sub/notes.txt":     true, // under /Logs/
		"/Users/foo/Library/Caches/myapp/cache.bin": false,
		"/Users/foo/work/Catalogs/index":            false, // similar but distinct segment
		"/var/log/system.log":                       true,
		"":                                          false,
	}
	for in, want := range cases {
		if got := isLogShapedFile(in); got != want {
			t.Errorf("isLogShapedFile(%q) = %v, want %v", in, got, want)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// walkLogsSerial is the filepath.Walk reference the concurrent walker must match.
func walkLogsSerial(root, owner string) []LogFile {
	var out []LogFile
	_ = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr // mirrors the tolerant production walk
		}
		if fi.IsDir() || !isLogShapedFile(path) {
			return nil
		}
		out = append(out, logFileFor(path, owner, fi))
		return nil
	})
	return out
}

func sortedLogKeys(files []LogFile) []string {
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, fmt.Sprintf("%s|%s|%d", f.Path, f.Owner, f.SizeBytes))
	}
	sort.Strings(keys)
	return keys
}

func TestLogWalkerMatchesSerialWalk(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Logs")
	for d := range 6 {
		for f := range 5 {
			dir := filepath.Join(root, fmt.Sprintf("dev-%d", d), "nested", fmt.Sprintf("n%d", f%2))
			mustWrite(t, filepath.Join(dir, fmt.Sprintf("f%d.txt", f)), "log")
			mustWrite(t, filepath.Join(dir, fmt.Sprintf("f%d.log", f)), "log")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	mustWrite(t, filepath.Join(outside, "hidden.log"), "x")
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}

	want := sortedLogKeys(walkLogsSerial(root, "App"))
	for _, workers := range []int{1, 4, 16} {
		w := newLogWalker(workers)
		w.root(root, "App")
		got := sortedLogKeys(w.wait())
		if !slices.Equal(got, want) {
			t.Fatalf("workers=%d: got %d files, want %d\ngot  %v\nwant %v", workers, len(got), len(want), got, want)
		}
		for _, k := range got {
			if strings.Contains(k, "hidden.log") {
				t.Fatalf("workers=%d followed a symlinked directory: %s", workers, k)
			}
		}
	}
}

func TestLogWalkerRootFileAndMissingRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "single.log")
	mustWrite(t, file, "x")
	w := newLogWalker(2)
	w.root(file, "")
	w.root(filepath.Join(dir, "missing"), "")
	got := w.wait()
	if len(got) != 1 || got[0].Path != file {
		t.Fatalf("got %+v, want only %s", got, file)
	}
}
