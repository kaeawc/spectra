package procdiag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// thr describes one thread's /proc state.
type thr struct {
	tid          int
	comm, state  string
	utime, stime int64
	wchan        string
	syscall      string // raw /proc/.../syscall text, "" = file absent
	vol, nonvol  int64
}

type proc struct {
	pid     int
	state   string
	threads []thr
	fds     int
	nofile  string
	rssKiB  int64
	swapKiB int64
	read    int64
	write   int64
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// statLine builds a /proc stat line; utime and stime are fields 14 and 15.
func statLine(id int, comm, state string, ut, st int64) string {
	return fmt.Sprintf("%d (%s) %s 1 1 1 0 -1 0 0 0 0 0 %d %d 0 0 20 0 1 0 100 0 0 0\n", id, comm, state, ut, st)
}

func writeProc(t *testing.T, root string, p proc) {
	t.Helper()
	base := filepath.Join(root, fmt.Sprint(p.pid))
	// Stale thread dirs from an earlier phase must not linger.
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}
	comm := "app"
	if len(p.threads) > 0 {
		comm = p.threads[0].comm
	}
	state := p.state
	if state == "" {
		state = "S"
	}
	writeFile(t, filepath.Join(base, "stat"), statLine(p.pid, comm, state, 0, 0))
	writeFile(t, filepath.Join(base, "cmdline"), "/usr/bin/app\x00--serve\x00")
	for _, th := range p.threads {
		d := filepath.Join(base, "task", fmt.Sprint(th.tid))
		writeFile(t, filepath.Join(d, "stat"), statLine(th.tid, th.comm, th.state, th.utime, th.stime))
		writeFile(t, filepath.Join(d, "wchan"), orZero(th.wchan))
		if th.syscall != "" {
			writeFile(t, filepath.Join(d, "syscall"), th.syscall+"\n")
		}
		writeFile(t, filepath.Join(d, "status"), fmt.Sprintf("voluntary_ctxt_switches:\t%d\nnonvoluntary_ctxt_switches:\t%d\n", th.vol, th.nonvol))
	}
	for i := 0; i < p.fds; i++ {
		writeFile(t, filepath.Join(base, "fd", fmt.Sprint(i)), "")
	}
	if p.nofile == "" {
		p.nofile = "1024"
	}
	writeFile(t, filepath.Join(base, "limits"), "Limit                     Soft Limit           Hard Limit           Units\nMax open files            "+p.nofile+"                  4096                 files\n")
	writeFile(t, filepath.Join(base, "status"), fmt.Sprintf("VmRSS:\t%d kB\nRssAnon:\t%d kB\nVmSwap:\t%d kB\n", p.rssKiB, p.rssKiB, p.swapKiB))
	writeFile(t, filepath.Join(base, "io"), fmt.Sprintf("rchar: 0\nread_bytes: %d\nwrite_bytes: %d\n", p.read, p.write))
	writeFile(t, filepath.Join(base, "oom_score"), "7\n")
}

func orZero(s string) string {
	if s == "" {
		return "0\n"
	}
	return s + "\n"
}

// run inspects with phase 1 on disk, switches to phase 2 during the sleep,
// and uses a fake clock so a "1s" interval is exactly 1s.
func run(t *testing.T, before, after proc) Report {
	t.Helper()
	root := t.TempDir()
	writeProc(t, root, before)
	clock := time.Unix(1000, 0)
	r, err := Inspect(context.Background(), before.pid, Options{
		ProcRoot: root, CgroupRoot: filepath.Join(root, "nocg"), Arch: "amd64", Interval: time.Second,
		Sleep: func(context.Context, time.Duration) error {
			writeProc(t, root, after)
			clock = clock.Add(time.Second)
			return nil
		},
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func find(r Report, kind string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].Kind == kind {
			return &r.Findings[i]
		}
	}
	return nil
}

func TestCPUSpin(t *testing.T) {
	b := proc{pid: 10, fds: 3, threads: []thr{{tid: 10, comm: "spin", state: "R", utime: 100, syscall: "running"}}}
	a := proc{pid: 10, fds: 3, threads: []thr{{tid: 10, comm: "spin", state: "R", utime: 199, stime: 0, syscall: "running"}}}
	r := run(t, b, a)
	f := find(r, KindCPUSpin)
	if f == nil || r.Verdict != KindCPUSpin {
		t.Fatalf("verdict=%q findings=%+v", r.Verdict, r.Findings)
	}
	if r.CPUPct < 98 || r.CPUPct > 100 {
		t.Fatalf("cpu = %.1f", r.CPUPct)
	}
	if !strings.Contains(f.Summary, "user-space loop") {
		t.Fatalf("summary = %q", f.Summary)
	}
}

func TestSyscallStorm(t *testing.T) {
	b := proc{pid: 10, threads: []thr{{tid: 10, comm: "x", state: "R", utime: 0, stime: 0}}}
	a := proc{pid: 10, threads: []thr{{tid: 10, comm: "x", state: "R", utime: 10, stime: 90}}}
	f := find(run(t, b, a), KindCPUSpin)
	if f == nil || !strings.Contains(f.Summary, "syscall storm") {
		t.Fatalf("finding = %+v", f)
	}
}

func TestFDLeakWithETA(t *testing.T) {
	th := []thr{{tid: 20, comm: "leaky", state: "S", wchan: "hrtimer_nanosleep", syscall: "230 0x0"}}
	b := proc{pid: 20, fds: 100, nofile: "1000", threads: th}
	a := proc{pid: 20, fds: 200, nofile: "1000", threads: th}
	r := run(t, b, a)
	f := find(r, KindFDGrowth)
	if f == nil {
		t.Fatalf("findings = %+v", r.Findings)
	}
	if r.FDs.RatePerSec != 100 || !strings.Contains(f.Summary, "limit is reached in 8s") {
		t.Fatalf("fds=%+v summary=%q", r.FDs, f.Summary)
	}
}

func TestFDExhaustionIsCritical(t *testing.T) {
	th := []thr{{tid: 20, comm: "x", state: "S"}}
	r := run(t, proc{pid: 20, fds: 970, nofile: "1000", threads: th}, proc{pid: 20, fds: 970, nofile: "1000", threads: th})
	f := find(r, KindFDExhaustion)
	if f == nil || f.Severity != SeverityCritical || r.Verdict != KindFDExhaustion {
		t.Fatalf("verdict=%q f=%+v", r.Verdict, f)
	}
	if find(r, KindFDGrowth) != nil {
		t.Fatal("steady fd count must not report growth")
	}
}

func TestUninterruptibleOnlyWhenPersistent(t *testing.T) {
	d := proc{pid: 30, threads: []thr{{tid: 30, comm: "io", state: "D", wchan: "nfs_wait"}}}
	r := run(t, d, d)
	f := find(r, KindUninterrupt)
	if f == nil || f.Severity != SeverityCritical || !strings.Contains(f.Evidence[0], "nfs_wait") {
		t.Fatalf("findings = %+v", r.Findings)
	}
	s := proc{pid: 30, threads: []thr{{tid: 30, comm: "io", state: "S"}}}
	if find(run(t, d, s), KindUninterrupt) != nil {
		t.Fatal("a single D reading is ordinary disk wait and must not be flagged")
	}
}

func TestLockWait(t *testing.T) {
	mk := func(vol int64) proc {
		return proc{pid: 40, threads: []thr{
			{tid: 40, comm: "app", state: "S", syscall: "202 0x1", wchan: "futex_do_wait", vol: vol},
			{tid: 41, comm: "worker", state: "S", syscall: "202 0x1", wchan: "futex_do_wait", vol: vol},
		}}
	}
	r := run(t, mk(5), mk(5))
	if f := find(r, KindLockWait); f == nil || !strings.Contains(f.Summary, "all 2 threads") {
		t.Fatalf("findings = %+v", r.Findings)
	}
	if find(run(t, mk(5), mk(6)), KindLockWait) != nil {
		t.Fatal("a thread that was scheduled during the interval is not stuck")
	}
}

func TestLockWaitFallsBackToWchan(t *testing.T) {
	mk := proc{pid: 40, threads: []thr{
		{tid: 40, comm: "app", state: "S", wchan: "futex_wait_queue"},
		{tid: 41, comm: "w", state: "S", wchan: "futex_wait_queue"},
	}}
	if find(run(t, mk, mk), KindLockWait) == nil {
		t.Fatal("wchan alone should be enough when the syscall file is unreadable")
	}
}

func TestIdleServerIsNotBlocked(t *testing.T) {
	mk := proc{pid: 50, threads: []thr{
		{tid: 50, comm: "srv", state: "S", syscall: "232 0x3"}, // epoll_wait
		{tid: 51, comm: "w", state: "S", syscall: "202 0x1"},   // futex
	}}
	r := run(t, mk, mk)
	if len(r.Findings) != 0 || r.Verdict != KindIdle {
		t.Fatalf("verdict=%q findings=%+v", r.Verdict, r.Findings)
	}
	if r.Threads[0].Syscall == "" {
		t.Fatalf("threads = %+v", r.Threads)
	}
}

func TestBlockedConnect(t *testing.T) {
	mk := proc{pid: 60, threads: []thr{{tid: 60, comm: "cli", state: "S", syscall: "42 0x3", wchan: "inet_stream_connect"}}}
	r := run(t, mk, mk)
	f := find(r, KindBlockedSys)
	if f == nil || f.Severity != SeverityWarning || !strings.Contains(f.Summary, "connect()") {
		t.Fatalf("findings = %+v", r.Findings)
	}
}

func TestBlockedReadIsOnlyInfo(t *testing.T) {
	mk := proc{pid: 61, threads: []thr{{tid: 61, comm: "cat", state: "S", syscall: "0 0x0"}}}
	f := find(run(t, mk, mk), KindBlockedSys)
	if f == nil || f.Severity != SeverityInfo {
		t.Fatalf("finding = %+v", f)
	}
}

func TestZombieAndStopped(t *testing.T) {
	z := proc{pid: 70, state: "Z", threads: []thr{{tid: 70, comm: "defunct", state: "Z"}}}
	if r := run(t, z, z); r.Verdict != KindZombie || len(r.Findings) != 1 {
		t.Fatalf("zombie: %+v", r.Findings)
	}
	s := proc{pid: 71, state: "T", threads: []thr{{tid: 71, comm: "stopped", state: "T"}}}
	if r := run(t, s, s); r.Verdict != KindStopped {
		t.Fatalf("stopped verdict = %q", r.Verdict)
	}
}

func TestMemoryGrowthAndSwap(t *testing.T) {
	th := []thr{{tid: 80, comm: "grow", state: "S"}}
	r := run(t, proc{pid: 80, threads: th, rssKiB: 100 * 1024}, proc{pid: 80, threads: th, rssKiB: 120 * 1024, swapKiB: 400 * 1024})
	if f := find(r, KindMemoryGrowth); f == nil || !strings.Contains(f.Summary, "20.0 MiB/s") {
		t.Fatalf("findings = %+v", r.Findings)
	}
	if find(r, KindSwappedOut) == nil {
		t.Fatalf("expected swapped-out info: %+v", r.Findings)
	}
}

func TestThreadsAreRankedAndCapped(t *testing.T) {
	var before, after []thr
	for i := 0; i < 40; i++ {
		before = append(before, thr{tid: 100 + i, comm: "w", state: "S"})
		after = append(after, thr{tid: 100 + i, comm: "w", state: "S", utime: int64(i)})
	}
	r := run(t, proc{pid: 100, threads: before}, proc{pid: 100, threads: after})
	if r.ThreadCount != 40 || len(r.Threads) != maxReportedThreads {
		t.Fatalf("count=%d reported=%d", r.ThreadCount, len(r.Threads))
	}
	if r.Threads[0].TID != 139 {
		t.Fatalf("hottest thread should lead, got tid %d", r.Threads[0].TID)
	}
}

func TestNoProcess(t *testing.T) {
	_, err := Inspect(context.Background(), 424242, Options{ProcRoot: t.TempDir()})
	if !errors.Is(err, ErrNoProcess) {
		t.Fatalf("err = %v", err)
	}
}

func TestProcessExitsBetweenSamples(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, proc{pid: 5, threads: []thr{{tid: 5, comm: "x", state: "S"}}})
	_, err := Inspect(context.Background(), 5, Options{ProcRoot: root, Sleep: func(context.Context, time.Duration) error {
		return os.RemoveAll(filepath.Join(root, "5"))
	}})
	if !errors.Is(err, ErrNoProcess) {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingOptionalFilesDegradeQuietly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "9", "stat"), statLine(9, "bare", "S", 0, 0))
	writeFile(t, filepath.Join(root, "9", "task", "9", "stat"), statLine(9, "bare", "S", 0, 0))
	r, err := Inspect(context.Background(), 9, Options{ProcRoot: root, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if r.Verdict != KindIdle || r.FDs.Count != 0 || r.IO != nil {
		t.Fatalf("report = %+v", r)
	}
	if len(r.Notes) == 0 {
		t.Fatal("unreadable fd table should be noted")
	}
}

func TestParseStatCommWithParens(t *testing.T) {
	st, ok := parseStat([]byte(statLine(1, "a) (b c", "R", 7, 9)))
	if !ok || st.comm != "a) (b c" || st.utime != 7 || st.stime != 9 || st.state != "R" {
		t.Fatalf("st = %+v ok=%v", st, ok)
	}
	if _, ok := parseStat([]byte("garbage")); ok {
		t.Fatal("garbage must not parse")
	}
}

func TestParseSyscallNr(t *testing.T) {
	for in, want := range map[string]int{"running\n": -1, "-1 0x7ffc 0x55\n": -1, "202 0x1 0x2\n": 202, "": -2, "x": -2} {
		if got := parseSyscallNr([]byte(in)); got != want {
			t.Errorf("%q = %d, want %d", in, got, want)
		}
	}
}

func TestSyscallName(t *testing.T) {
	if SyscallName("amd64", 202) != "futex" || SyscallName("arm64", 98) != "futex" || SyscallName("amd64", 45) != "recvfrom" {
		t.Fatal("bad table")
	}
	if SyscallName("riscv64", 1) != "syscall_1" || SyscallName("amd64", 99999) != "syscall_99999" {
		t.Fatal("unknown fallback wrong")
	}
}

// TestInspectSelf exercises the real procfs end to end.
func TestInspectSelf(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("needs Linux procfs")
	}
	r, err := Inspect(context.Background(), os.Getpid(), Options{Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if r.PID != os.Getpid() || r.ThreadCount < 1 || r.FDs.Count < 3 || r.Memory.RSSKiB <= 0 {
		t.Fatalf("report = %+v", r)
	}
}
