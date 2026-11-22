//go:build darwin

package storagestate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func logKeysWithMTime(files []LogFile) []string {
	keys := make([]string, 0, len(files))
	for _, f := range files {
		keys = append(keys, fmt.Sprintf("%s|%s|%d|%d", f.Path, f.Owner, f.SizeBytes, f.ModTime.UnixNano()))
	}
	sort.Strings(keys)
	return keys
}

// TestReadLogDirBulkMatchesPortable compares the getattrlistbulk log reader
// with the ReadDir path directory by directory, including mtimes.
func TestReadLogDirBulkMatchesPortable(t *testing.T) {
	for _, base := range []string{t.TempDir(), mkTmpUnder(t, "/tmp")} {
		root := filepath.Join(base, "Library")
		logs := filepath.Join(root, "Logs", "App")
		writeSized(t, filepath.Join(logs, "plain.txt"), 10) // under /Logs/: log-shaped
		writeSized(t, filepath.Join(logs, "big.log"), 70_000)
		writeSized(t, filepath.Join(root, "Other", "a.log"), 5)
		writeSized(t, filepath.Join(root, "Other", "a.txt"), 5)
		for i := range 3000 { // several getattrlistbulk batches
			writeSized(t, filepath.Join(root, "Other", fmt.Sprintf("entry-with-a-longish-name-%05d.log", i)), i%3)
		}
		if err := os.Symlink(logs, filepath.Join(root, "Other", "link.log")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(logs, filepath.Join(root, "Logs", "linkdir")); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{root, filepath.Join(root, "Logs"), logs, filepath.Join(root, "Other")} {
			bDirs, bFiles, ok := readLogDirBulk(dir, "Owner")
			if !ok {
				t.Fatalf("%s: bulk reader fell back on an ordinary directory", dir)
			}
			pDirs, pFiles := readLogDirPortable(dir, "Owner")
			slices.Sort(bDirs)
			slices.Sort(pDirs)
			if !slices.Equal(bDirs, pDirs) {
				t.Errorf("%s: dirs bulk = %v, portable = %v", dir, bDirs, pDirs)
			}
			if got, want := logKeysWithMTime(bFiles), logKeysWithMTime(pFiles); !slices.Equal(got, want) {
				t.Errorf("%s: %d bulk files, %d portable\nbulk     %.5v\nportable %.5v", dir, len(got), len(want), got, want)
			}
		}
	}
}

func TestReadLogDirBulkMissingDir(t *testing.T) {
	dirs, files, ok := readLogDirBulk(filepath.Join(t.TempDir(), "nope"), "")
	if !ok || dirs != nil || files != nil {
		t.Errorf("missing dir: dirs=%v files=%v ok=%v, want nothing handled", dirs, files, ok)
	}
}

func TestDecodeBulkWithoutNames(t *testing.T) {
	buf := bulkRecord("file", vREG, 4096)
	got, err := decodeBulkRecords(buf, 1, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].name != "" || string(got[0].raw) != "file" || got[0].alloc != 4096 {
		t.Errorf("decodeBulkRecords = %+v", got)
	}
}

func mkTmpUnder(t *testing.T, dir string) string {
	t.Helper()
	d, err := os.MkdirTemp(dir, "storagestate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}
