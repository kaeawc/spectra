package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// writeProcFixture builds a minimal procfs tree under a temp dir and
// returns its root. btime is the "btime" line written to <root>/stat.
func writeProcFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "stat"), "cpu  1 2 3\nbtime 1600000000\n")

	pidDir := filepath.Join(root, "1234")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// pid(1234) comm(my proc) state(S) ppid(1) ... utime(50) stime(30)
	// ... num_threads(8) ... starttime(1000 ticks) vsize(123456789 bytes)
	// rss(2048 pages). comm deliberately contains a space and parens.
	stat := "1234 (my (proc)) S 1 1234 1234 0 -1 4194560 100 0 0 0 50 30 0 0 20 0 8 0 1000 123456789 2048 0 0 0 0 0 0 0 0\n"
	mustWrite(t, filepath.Join(pidDir, "stat"), stat)
	mustWrite(t, filepath.Join(pidDir, "status"), "Name:\tmy (proc)\nUid:\t1000\t1000\t1000\t1000\n")
	mustWrite(t, filepath.Join(pidDir, "cmdline"), "/usr/bin/myproc\x00--flag\x00value\x00")

	// A kernel-thread-style entry: empty cmdline.
	kDir := filepath.Join(root, "2")
	if err := os.MkdirAll(kDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(kDir, "stat"), "2 (kthreadd) S 0 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0 0 0 0 0 0 0\n")
	mustWrite(t, filepath.Join(kDir, "status"), "Uid:\t0\t0\t0\t0\n")
	mustWrite(t, filepath.Join(kDir, "cmdline"), "")
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectLinuxProcs(t *testing.T) {
	root := writeProcFixture(t)
	now := func() time.Time { return time.Unix(1600000110, 0) } // start(1600000010)+100s
	procs := collectLinuxProcs(root, now)

	byPID := map[int]Info{}
	for _, p := range procs {
		byPID[p.PID] = p
	}

	p := byPID[1234]
	if p.PID != 1234 {
		t.Fatalf("pid 1234 not collected; got %+v", procs)
	}
	if p.PPID != 1 {
		t.Errorf("PPID = %d, want 1", p.PPID)
	}
	if p.Command != "my (proc)" {
		t.Errorf("Command = %q, want %q (comm with spaces/parens)", p.Command, "my (proc)")
	}
	if p.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d, want 8", p.ThreadCount)
	}
	if p.FullCommandLine != "/usr/bin/myproc --flag value" {
		t.Errorf("FullCommandLine = %q", p.FullCommandLine)
	}
	if p.UID != 1000 {
		t.Errorf("UID = %d, want 1000", p.UID)
	}
	wantRSS := int64(2048) * int64(os.Getpagesize()) / 1024
	if p.RSSKiB != wantRSS {
		t.Errorf("RSSKiB = %d, want %d", p.RSSKiB, wantRSS)
	}
	if p.VSizeKiB != 123456789/1024 {
		t.Errorf("VSizeKiB = %d, want %d", p.VSizeKiB, int64(123456789/1024))
	}
	// cpu%: (50+30)/100 ticks = 0.8s over 100s elapsed = 0.8%.
	if p.CPUPct < 0.79 || p.CPUPct > 0.81 {
		t.Errorf("CPUPct = %v, want ~0.8", p.CPUPct)
	}
	if !p.StartTime.Equal(time.Unix(1600000010, 0)) {
		t.Errorf("StartTime = %v, want 1600000010", p.StartTime.Unix())
	}

	k := byPID[2]
	if k.FullCommandLine != "[kthreadd]" {
		t.Errorf("kernel thread FullCommandLine = %q, want [kthreadd]", k.FullCommandLine)
	}
}

func TestCollectAllLinuxBranch(t *testing.T) {
	root := writeProcFixture(t)
	procs := CollectAll(context.Background(), CollectOptions{OS: hostos.Linux, ProcFS: root})
	if len(procs) != 2 {
		t.Fatalf("CollectAll(Linux) returned %d procs, want 2", len(procs))
	}
}

func TestParseLinuxStatRejectsGarbage(t *testing.T) {
	if _, ok := parseLinuxStat([]byte("not a stat line")); ok {
		t.Error("parseLinuxStat accepted garbage")
	}
}

func TestParseBtime(t *testing.T) {
	bt, ok := parseBtime([]byte("cpu 1 2 3\nbtime 1700000000\nprocs_running 1\n"))
	if !ok || bt.Unix() != 1700000000 {
		t.Errorf("parseBtime = %v %v, want 1700000000 true", bt.Unix(), ok)
	}
	if _, ok := parseBtime([]byte("no btime here\n")); ok {
		t.Error("parseBtime accepted input without btime")
	}
}
