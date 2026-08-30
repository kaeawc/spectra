package leveldbcheck

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// fakeFS builds Deps from an in-memory layout: dir -> entries, and file -> body.
type fakeFS struct {
	dirs  map[string][]Entry
	files map[string]string
}

func (f fakeFS) deps() Deps {
	return Deps{
		ReadDir: func(dir string) ([]Entry, error) {
			e, ok := f.dirs[dir]
			if !ok {
				return nil, errors.New("no such directory")
			}
			return e, nil
		},
		ReadFile: func(path string) ([]byte, error) {
			b, ok := f.files[path]
			if !ok {
				return nil, errors.New("no such file")
			}
			return []byte(b), nil
		},
	}
}

func file(name string, size int64) Entry { return Entry{Name: name, Size: size} }

func TestInspectHealthyStore(t *testing.T) {
	fs := fakeFS{
		dirs: map[string][]Entry{
			"/store": {
				file("CURRENT", 16),
				file("MANIFEST-000001", 200),
				file("000003.ldb", 4096),
				file("000004.ldb", 8192),
				file("000005.log", 1024),
				{Name: "LOCK", Size: 0},
			},
		},
		files: map[string]string{"/store/CURRENT": "MANIFEST-000001\n"},
	}
	s := Inspect("/store", fs.deps())
	if len(s.Problems) != 0 {
		t.Fatalf("healthy store should have no problems, got %v", s.Problems)
	}
	if !s.ManifestOK || s.ManifestFile != "MANIFEST-000001" {
		t.Errorf("manifest = %q ok=%v", s.ManifestFile, s.ManifestOK)
	}
	if s.TableFiles != 2 || s.TableBytes != 12288 {
		t.Errorf("tables = %d/%d, want 2/12288", s.TableFiles, s.TableBytes)
	}
	if s.LogFiles != 1 || s.LogBytes != 1024 {
		t.Errorf("logs = %d/%d, want 1/1024", s.LogFiles, s.LogBytes)
	}
	if s.TotalBytes != 16+200+4096+8192+1024 {
		t.Errorf("total = %d", s.TotalBytes)
	}
}

func TestInspectMissingManifest(t *testing.T) {
	fs := fakeFS{
		dirs:  map[string][]Entry{"/store": {file("CURRENT", 16), file("000003.ldb", 10)}},
		files: map[string]string{"/store/CURRENT": "MANIFEST-999999\n"},
	}
	s := Inspect("/store", fs.deps())
	if s.ManifestOK {
		t.Error("manifest should be reported missing")
	}
	if !hasProblem(s, "missing manifest MANIFEST-999999") {
		t.Errorf("expected missing-manifest problem, got %v", s.Problems)
	}
}

func TestInspectNoCurrent(t *testing.T) {
	fs := fakeFS{dirs: map[string][]Entry{"/store": {file("random.txt", 5)}}}
	s := Inspect("/store", fs.deps())
	if !hasProblem(s, "no CURRENT file") {
		t.Errorf("expected no-CURRENT problem, got %v", s.Problems)
	}
}

func TestInspectCompactionBacklog(t *testing.T) {
	entries := []Entry{file("CURRENT", 16), file("MANIFEST-1", 10)}
	for i := 0; i < compactionBacklogTables+1; i++ {
		entries = append(entries, file("t"+strconv.Itoa(i)+".ldb", 100))
	}
	fs := fakeFS{
		dirs:  map[string][]Entry{"/store": entries},
		files: map[string]string{"/store/CURRENT": "MANIFEST-1"},
	}
	s := Inspect("/store", fs.deps())
	if !hasProblem(s, "compaction backlog") {
		t.Errorf("expected compaction-backlog problem, got %v", s.Problems)
	}
}

func TestInspectLogBloat(t *testing.T) {
	fs := fakeFS{
		dirs: map[string][]Entry{"/store": {
			file("CURRENT", 16), file("MANIFEST-1", 10),
			file("000009.log", logBloatThreshold+1),
		}},
		files: map[string]string{"/store/CURRENT": "MANIFEST-1"},
	}
	s := Inspect("/store", fs.deps())
	if !hasProblem(s, "write-ahead log bloat") {
		t.Errorf("expected log-bloat problem, got %v", s.Problems)
	}
}

func TestInspectUnreadableDir(t *testing.T) {
	s := Inspect("/missing", fakeFS{dirs: map[string][]Entry{}}.deps())
	if s.Error == "" {
		t.Error("expected an error for an unreadable directory")
	}
}

func TestCheckCountsProblems(t *testing.T) {
	fs := fakeFS{
		dirs: map[string][]Entry{
			"/good": {file("CURRENT", 16), file("MANIFEST-1", 10)},
			"/bad":  {file("CURRENT", 16)},
		},
		files: map[string]string{"/good/CURRENT": "MANIFEST-1", "/bad/CURRENT": "MANIFEST-missing"},
	}
	rep := Check([]string{"/good", "/bad"}, fs.deps())
	if rep.Scanned != 2 || rep.Problems != 1 {
		t.Fatalf("scanned=%d problems=%d, want 2/1", rep.Scanned, rep.Problems)
	}
}

func TestDiscover(t *testing.T) {
	fs := fakeFS{
		dirs: map[string][]Entry{
			"/root":                               {{Name: "IndexedDB", IsDir: true}, {Name: "notes.txt"}},
			"/root/IndexedDB":                     {{Name: "a.indexeddb.leveldb", IsDir: true}},
			"/root/IndexedDB/a.indexeddb.leveldb": {file("CURRENT", 16), file("MANIFEST-1", 10)},
		},
	}
	stores := Discover("/root", fs.deps())
	if len(stores) != 1 || stores[0] != "/root/IndexedDB/a.indexeddb.leveldb" {
		t.Errorf("discover = %v, want the one leveldb dir", stores)
	}
}

func hasProblem(s Store, substr string) bool {
	for _, p := range s.Problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}
