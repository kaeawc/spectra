package snapshot

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/heapdump"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("JAVA PROFILE 1.0.2"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectHeapDumpsFromHeapDumpPath(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "java_pid10.hprof"))
	jvms := []jvm.Info{{PID: 10, MainClass: "svc.App", VMArgs: "-XX:+HeapDumpOnOutOfMemoryError -XX:HeapDumpPath=" + dir}}

	got := collectHeapDumps(heapdump.OSFS{}, jvms, nil)
	if len(got) != 1 || got[0].PID != 10 || got[0].MainClass != "svc.App" || got[0].DumpPID != 10 {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectHeapDumpsPrefersProcessCwdOverUserDir(t *testing.T) {
	cwd, userDir := t.TempDir(), t.TempDir()
	touch(t, filepath.Join(cwd, "java_pid10.hprof"))
	touch(t, filepath.Join(userDir, "java_pid11.hprof"))
	jvms := []jvm.Info{{PID: 10, SysProps: map[string]string{"user.dir": userDir}}}
	procs := []process.Info{{PID: 10, Cwd: cwd}}

	got := collectHeapDumps(heapdump.OSFS{}, jvms, procs)
	if len(got) != 1 || got[0].Path != filepath.Join(cwd, "java_pid10.hprof") {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectHeapDumpsFallsBackToUserDir(t *testing.T) {
	userDir := t.TempDir()
	touch(t, filepath.Join(userDir, "java_pid10.hprof"))
	jvms := []jvm.Info{{PID: 10, SysProps: map[string]string{"user.dir": userDir}}}

	got := collectHeapDumps(heapdump.OSFS{}, jvms, nil)
	if len(got) != 1 || got[0].Source != heapdump.SourceWorkingDir {
		t.Fatalf("got %+v", got)
	}
}

func TestCollectHeapDumpsSharedDirAttributesByPID(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "java_pid20.hprof"))
	args := "-XX:HeapDumpPath=" + dir
	jvms := []jvm.Info{
		{PID: 10, MainClass: "a.A", VMArgs: args},
		{PID: 20, MainClass: "b.B", VMArgs: args},
	}

	got := collectHeapDumps(heapdump.OSFS{}, jvms, nil)
	if len(got) != 1 || got[0].PID != 20 || got[0].MainClass != "b.B" {
		t.Fatalf("expected a single dump attributed to PID 20, got %+v", got)
	}
}

func TestCollectHeapDumpsNoJVMs(t *testing.T) {
	if got := collectHeapDumps(heapdump.OSFS{}, nil, nil); got != nil {
		t.Fatalf("got %+v", got)
	}
}
