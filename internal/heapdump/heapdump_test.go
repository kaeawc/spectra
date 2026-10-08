package heapdump

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestHeapDumpPathFromArgs(t *testing.T) {
	cases := map[string]string{
		"":                            "",
		"-Xmx1g":                      "",
		"-XX:HeapDumpPath=/var/dumps": "/var/dumps",
		"-XX:HeapDumpPath=/a -XX:HeapDumpPath=/b": "/b",
		`-XX:HeapDumpPath="/q/x.hprof" -Xmx1g`:    "/q/x.hprof",
	}
	for in, want := range cases {
		if got := HeapDumpPathFromArgs(in); got != want {
			t.Errorf("HeapDumpPathFromArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPIDFromName(t *testing.T) {
	cases := map[string]int{
		"java_pid123.hprof": 123,
		"java_pid.hprof":    0,
		"java_pidx.hprof":   0,
		"java_pid0.hprof":   0,
		"heap.hprof":        0,
		"java_pid12.txt":    0,
	}
	for name, want := range cases {
		got, ok := PIDFromName(name)
		if got != want || ok != (want > 0) {
			t.Errorf("PIDFromName(%q) = %d,%v want %d", name, got, ok, want)
		}
	}
}

func TestFindHeapDumpPathDirectory(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(time.Hour)
	writeFile(t, filepath.Join(dir, "java_pid42.hprof"), 10, recent)
	writeFile(t, filepath.Join(dir, "custom.hprof"), 5, old)
	writeFile(t, filepath.Join(dir, "notes.txt"), 1, old)
	if err := os.Mkdir(filepath.Join(dir, "sub.hprof"), 0o755); err != nil {
		t.Fatal(err)
	}

	dumps := Find(OSFS{}, Request{PID: 42, HeapDumpPath: dir})
	if len(dumps) != 2 {
		t.Fatalf("expected 2 dumps, got %+v", dumps)
	}
	if dumps[0].Path != filepath.Join(dir, "java_pid42.hprof") || dumps[0].DumpPID != 42 ||
		dumps[0].SizeBytes != 10 || !dumps[0].ModTime.Equal(recent) || dumps[0].Source != SourceHeapDumpPath {
		t.Fatalf("dumps[0] = %+v", dumps[0])
	}
	if dumps[1].Path != filepath.Join(dir, "custom.hprof") || dumps[1].DumpPID != 0 {
		t.Fatalf("dumps[1] = %+v", dumps[1])
	}
}

func TestFindHeapDumpPathFileWithPIDPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "svc-7.hprof"), 3, time.Now())
	dumps := Find(OSFS{}, Request{PID: 7, HeapDumpPath: filepath.Join(dir, "svc-%p.hprof")})
	if len(dumps) != 1 || dumps[0].Path != filepath.Join(dir, "svc-7.hprof") {
		t.Fatalf("dumps = %+v", dumps)
	}
}

func TestFindRelativeHeapDumpPathAnchoredAtWorkingDir(t *testing.T) {
	wd := t.TempDir()
	if err := os.Mkdir(filepath.Join(wd, "dumps"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wd, "dumps", "x.hprof"), 1, time.Now())
	dumps := Find(OSFS{}, Request{PID: 1, HeapDumpPath: "dumps", WorkingDir: wd})
	if len(dumps) != 1 || dumps[0].Path != filepath.Join(wd, "dumps", "x.hprof") {
		t.Fatalf("dumps = %+v", dumps)
	}
	if got := Find(OSFS{}, Request{PID: 1, HeapDumpPath: "dumps"}); got != nil {
		t.Fatalf("relative path without working dir should yield nil, got %+v", got)
	}
}

func TestFindWorkingDirOnlyDefaultNames(t *testing.T) {
	wd := t.TempDir()
	writeFile(t, filepath.Join(wd, "java_pid99.hprof"), 1, time.Now())
	writeFile(t, filepath.Join(wd, "fixture.hprof"), 1, time.Now())
	dumps := Find(OSFS{}, Request{PID: 5, WorkingDir: wd})
	if len(dumps) != 1 || dumps[0].DumpPID != 99 || dumps[0].Source != SourceWorkingDir {
		t.Fatalf("dumps = %+v", dumps)
	}
}

func TestFindHeapDumpPathSkipsWorkingDir(t *testing.T) {
	wd := t.TempDir()
	writeFile(t, filepath.Join(wd, "java_pid5.hprof"), 1, time.Now())
	if got := Find(OSFS{}, Request{PID: 5, HeapDumpPath: filepath.Join(wd, "missing"), WorkingDir: wd}); got != nil {
		t.Fatalf("configured path should take precedence, got %+v", got)
	}
}

func TestFindNothingConfigured(t *testing.T) {
	if got := Find(OSFS{}, Request{PID: 5}); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestFindCapsAtMaxDumps(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxDumps+5; i++ {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("d%02d.hprof", i)), 1, base.Add(time.Duration(i)*time.Minute))
	}
	dumps := Find(OSFS{}, Request{PID: 1, HeapDumpPath: dir})
	if len(dumps) != MaxDumps {
		t.Fatalf("expected %d dumps, got %d", MaxDumps, len(dumps))
	}
	if filepath.Base(dumps[0].Path) != fmt.Sprintf("d%02d.hprof", MaxDumps+4) {
		t.Fatalf("newest first expected, got %s", dumps[0].Path)
	}
}

type fakeFS struct {
	dirInfo fs.FileInfo
	readN   int
	readErr error
}

func (f *fakeFS) Stat(string) (fs.FileInfo, error) { return f.dirInfo, nil }

func (f *fakeFS) ReadDir(_ string, n int) ([]fs.DirEntry, error) {
	f.readN = n
	return nil, f.readErr
}

func TestFindBoundsDirectoryRead(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFS{dirInfo: dirInfo, readErr: errors.New("permission denied")}
	if got := Find(f, Request{PID: 1, HeapDumpPath: "/dumps"}); got != nil {
		t.Fatalf("expected nil on read error, got %+v", got)
	}
	if f.readN != MaxDirEntries {
		t.Fatalf("ReadDir n = %d, want %d", f.readN, MaxDirEntries)
	}
}
