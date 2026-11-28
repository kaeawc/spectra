package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/contracttest"
	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/procdiag"
)

// procdiagNow is the first-sample time the procdiag cases inject.
const procdiagNow = "2026-11-23T10:00:00Z"

// procdiagThread is one thread's /proc state in a procdiag fixture.
type procdiagThread struct {
	tid          int
	comm, state  string
	utime, stime int64
	wchan        string
	syscall      string // raw /proc/.../syscall text; "" = file absent
	vol, nonvol  int64
}

// procdiagProc is one process's /proc state in a procdiag fixture.
type procdiagProc struct {
	pid      int
	state    string
	cmdline  string // raw, NUL-separated; "" = "/usr/bin/app\x00--serve\x00"
	cgroup   string // /proc/<pid>/cgroup; "" = a cgroup v1 line (no v2 accounting)
	threads  []procdiagThread
	fds      int
	nofile   string
	rssKiB   int64
	swapKiB  int64
	pssKiB   int64
	read     int64
	write    int64
	bareOnly bool // only stat, task stats and cgroup: every optional file missing
}

func procdiagStatLine(id int, comm, state string, ut, st int64) string {
	return fmt.Sprintf("%d (%s) %s 1 1 1 0 -1 0 0 0 0 0 %d %d 0 0 20 0 1 0 100 0 0 0\n", id, comm, state, ut, st)
}

// procdiagFiles is the /proc tree (relative to the case tree) for p.
func procdiagFiles(p procdiagProc) map[string]string {
	base := fmt.Sprintf("proc/%d/", p.pid)
	comm := "app"
	if len(p.threads) > 0 {
		comm = p.threads[0].comm
	}
	state := p.state
	if state == "" {
		state = "S"
	}
	cgroup := p.cgroup
	if cgroup == "" {
		cgroup = "1:name=systemd:/user.slice\n"
	}
	files := map[string]string{
		base + "stat":   procdiagStatLine(p.pid, comm, state, 0, 0),
		base + "cgroup": cgroup,
	}
	for _, th := range p.threads {
		d := fmt.Sprintf("%stask/%d/", base, th.tid)
		files[d+"stat"] = procdiagStatLine(th.tid, th.comm, th.state, th.utime, th.stime)
		if p.bareOnly {
			continue
		}
		files[d+"wchan"] = "0\n"
		if th.wchan != "" {
			files[d+"wchan"] = th.wchan + "\n"
		}
		if th.syscall != "" {
			files[d+"syscall"] = th.syscall + "\n"
		}
		files[d+"status"] = fmt.Sprintf("voluntary_ctxt_switches:\t%d\nnonvoluntary_ctxt_switches:\t%d\n", th.vol, th.nonvol)
	}
	if p.bareOnly {
		return files
	}
	cmdline := p.cmdline
	if cmdline == "" {
		cmdline = "/usr/bin/app\x00--serve\x00"
	}
	files[base+"cmdline"] = cmdline
	for i := 0; i < p.fds; i++ {
		files[fmt.Sprintf("%sfd/%d", base, i)] = ""
	}
	nofile := p.nofile
	if nofile == "" {
		nofile = "1024"
	}
	files[base+"limits"] = "Limit                     Soft Limit           Hard Limit           Units\nMax open files            " + nofile + "                  4096                 files\n"
	files[base+"status"] = fmt.Sprintf("VmRSS:\t%d kB\nRssAnon:\t%d kB\nVmSwap:\t%d kB\n", p.rssKiB, p.rssKiB, p.swapKiB)
	files[base+"io"] = fmt.Sprintf("rchar: 0\nread_bytes: %d\nwrite_bytes: %d\n", p.read, p.write)
	files[base+"oom_score"] = "7\n"
	if p.pssKiB > 0 {
		files[base+"smaps_rollup"] = fmt.Sprintf("Rss:  %d kB\nPss:  %d kB\n", p.rssKiB, p.pssKiB)
	}
	return files
}

// procdiagTree writes (or checks) files under dir and returns dir.
func procdiagTree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if *contracttest.Update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := fsutil.WriteFileAtomic(path, []byte(files[name]), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path) // #nosec G304 -- path is under contract/cases
		if err != nil {
			t.Fatalf("%s: %v (write it with -contract-update)", path, err)
		}
		if string(got) != files[name] {
			t.Fatalf("%s no longer matches the test fixture; rewrite it with -contract-update", path)
		}
	}
	return dir
}

func procdiagMerge(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// procdiagScenario is one diagnosis: the trees at the first and second samples.
type procdiagScenario struct {
	args          []string // after the mode flag
	before, after map[string]string
}

func procdiagSteady(p procdiagProc) procdiagScenario {
	f := procdiagFiles(p)
	return procdiagScenario{args: []string{fmt.Sprint(p.pid)}, before: f, after: f}
}

func procdiagChange(a, b procdiagProc) procdiagScenario {
	return procdiagScenario{args: []string{fmt.Sprint(a.pid)}, before: procdiagFiles(a), after: procdiagFiles(b)}
}

func procdiagScenarios() map[string]procdiagScenario {
	spin := func(ut int64) procdiagProc {
		return procdiagProc{pid: 10, fds: 3, read: ut * 4096, write: ut * 8192, threads: []procdiagThread{
			{tid: 10, comm: "spin", state: "R", utime: ut, stime: ut / 10, syscall: "running"},
			{tid: 11, comm: "spin-worker", state: "S", wchan: "futex_wait_queue", syscall: "202 0x1"},
		}}
	}
	leak := func(fds int) procdiagProc {
		return procdiagProc{pid: 20, fds: fds, nofile: "1000",
			cmdline: "/opt/service/bin/leaky-server\x00--config=/etc/leaky/config.yaml\x00--listen=0.0.0.0:8080\x00--workers=16\x00--log-level=debug\x00--trace\x00",
			threads: []procdiagThread{{tid: 20, comm: "leaky", state: "S", wchan: "hrtimer_nanosleep", syscall: "230 0x0"}}}
	}
	lock := procdiagProc{pid: 40, threads: []procdiagThread{
		{tid: 40, comm: "app", state: "S", syscall: "202 0x1", wchan: "futex_do_wait", vol: 5},
		{tid: 41, comm: "worker", state: "S", syscall: "202 0x1", wchan: "futex_do_wait", vol: 5},
	}}
	grow := func(rss, swap int64) procdiagProc {
		return procdiagProc{pid: 80, rssKiB: rss, swapKiB: swap, pssKiB: rss / 2, threads: []procdiagThread{{tid: 80, comm: "grow", state: "S"}}}
	}
	var before, after []procdiagThread
	for i := 0; i < 34; i++ {
		before = append(before, procdiagThread{tid: 100 + i, comm: fmt.Sprintf("pool-worker-thread-%d", i), state: "S"})
		state := "S"
		if i%7 == 0 {
			state = "R"
		}
		after = append(after, procdiagThread{tid: 100 + i, comm: fmt.Sprintf("pool-worker-thread-%d", i), state: state, utime: int64(i % 2)})
	}
	cg := procdiagProc{pid: 90, cgroup: "0::/app.slice\n", threads: []procdiagThread{{tid: 90, comm: "srv", state: "S", syscall: "232 0x3"}}}
	cgFiles := procdiagMerge(procdiagFiles(cg), map[string]string{
		"sys/fs/cgroup/app.slice/cgroup.controllers": "cpu memory pids\n",
		"sys/fs/cgroup/app.slice/cpu.stat":           "usage_usec 100\nnr_periods 100\nnr_throttled 80\nthrottled_usec 5000000\n",
		"sys/fs/cgroup/app.slice/cpu.pressure":       "some avg10=55.00 avg60=40.00 avg300=20.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n",
	})
	interval := procdiagChange(leak(5), leak(25))
	interval.args = []string{"--interval", "2s", "20"}
	return map[string]procdiagScenario{
		"cpu-spin":      procdiagChange(spin(100), spin(199)),
		"fd-growth":     interval,
		"fd-exhaustion": procdiagSteady(procdiagProc{pid: 21, fds: 10, nofile: "10", threads: []procdiagThread{{tid: 21, comm: "x", state: "S"}}}),
		"uninterruptible": procdiagSteady(procdiagProc{pid: 30, threads: []procdiagThread{
			{tid: 30, comm: "io", state: "D", wchan: "nfs_wait"}, {tid: 31, comm: "io-2", state: "D"},
		}}),
		"lock-wait":       procdiagSteady(lock),
		"blocked-connect": procdiagSteady(procdiagProc{pid: 60, threads: []procdiagThread{{tid: 60, comm: "cli", state: "S", syscall: "42 0x3", wchan: "inet_stream_connect"}}}),
		"blocked-read":    procdiagSteady(procdiagProc{pid: 61, threads: []procdiagThread{{tid: 61, comm: "cat", state: "S", syscall: "0 0x0"}}}),
		"zombie":          procdiagSteady(procdiagProc{pid: 70, state: "Z", threads: []procdiagThread{{tid: 70, comm: "defunct", state: "Z"}}}),
		"stopped":         procdiagSteady(procdiagProc{pid: 71, state: "T", threads: []procdiagThread{{tid: 71, comm: "stopped", state: "T"}}}),
		"memory-growth":   procdiagChange(grow(100*1024, 0), grow(120*1024, 400*1024)),
		"many-threads":    procdiagChange(procdiagProc{pid: 100, threads: before}, procdiagProc{pid: 100, threads: after}),
		"idle":            procdiagSteady(procdiagProc{pid: 50, threads: []procdiagThread{{tid: 50, comm: "srv", state: "S", syscall: "232 0x3"}, {tid: 51, comm: "w", state: "S", syscall: "202 0x1"}}}),
		"cgroup-context":  {args: []string{"90"}, before: cgFiles, after: cgFiles},
		"missing-files":   procdiagSteady(procdiagProc{pid: 9, bareOnly: true, threads: []procdiagThread{{tid: 9, comm: "bare", state: "S"}}}),
	}
}

// TestContractProcdiagCases records `spectra procdiag` (JSON and text) on
// Linux fixture trees sampled twice (before/ at the first sample, root/ at
// the second, which the analysis also reads), its errors, and the macOS
// refusal. Replayed by crates/cli/tests/contract.rs.
func TestContractProcdiagCases(t *testing.T) {
	t0, err := time.Parse(time.RFC3339, procdiagNow)
	if err != nil {
		t.Fatal(err)
	}
	const arch = "amd64"
	record := func(name, goos string, args []string, c contracttest.Case, beforeDir, afterDir string) {
		t.Helper()
		restore := hostos.SetForTest(map[string]hostos.Kind{"darwin": hostos.Darwin, "linux": hostos.Linux}[goos])
		defer restore()
		c.Input(t, map[string]any{"args": append([]string{"procdiag"}, args...), "os": goos, "arch": arch, "now": procdiagNow})
		link := filepath.Join(t.TempDir(), "cur")
		if err := os.Symlink(beforeDir, link); err != nil {
			t.Fatal(err)
		}
		inspect := func(ctx context.Context, pid int, o procdiag.Options) (procdiag.Report, error) {
			now := t0
			o.ProcRoot = filepath.Join(link, "proc")
			o.CgroupRoot = filepath.Join(link, "sys", "fs", "cgroup")
			o.Arch = arch
			o.Now = func() time.Time { return now }
			o.Sleep = func(_ context.Context, d time.Duration) error {
				if err := os.Remove(link); err != nil {
					return err
				}
				now = now.Add(d)
				return os.Symlink(afterDir, link)
			}
			return procdiag.Inspect(ctx, pid, o)
		}
		var stdout, stderr bytes.Buffer
		code := runProcdiagWithIO(args, &stdout, &stderr, inspect)
		c.Expect(t, stdout.Bytes(), code)
		expectStderr(t, c, stderr.Bytes())
	}
	for name, s := range procdiagScenarios() {
		for _, mode := range []string{"json", "text"} {
			c := contracttest.New(t, "procdiag", mode+"-"+name)
			beforeDir := procdiagTree(t, filepath.Join(c.Dir, "before"), s.before)
			afterDir := procdiagTree(t, filepath.Join(c.Dir, "root"), s.after)
			args := s.args
			if mode == "json" {
				args = append([]string{"--json"}, args...)
			}
			record(mode+"-"+name, "linux", args, c, beforeDir, afterDir)
		}
	}
	for name, args := range map[string][]string{
		"no-such-process":  {"424242"},
		"usage-no-pid":     {"--json"},
		"usage-two-pids":   {"1", "2"},
		"invalid-pid":      {"abc"},
		"invalid-pid-zero": {"0"},
		"interval-zero":    {"--interval", "0s", "5"},
		"interval-invalid": {"--interval=fast", "5"},
		"flag-unknown":     {"--nope", "5"},
		"flag-help":        {"-h"},
	} {
		c := contracttest.New(t, "procdiag", name)
		missing := filepath.Join(t.TempDir(), "missing")
		record(name, "linux", args, c, missing, missing)
	}
	for name, args := range map[string][]string{"darwin-json": {"--json", "5"}, "darwin-text": {"5"}} {
		c := contracttest.New(t, "procdiag", name)
		missing := filepath.Join(t.TempDir(), "missing")
		record(name, "darwin", args, c, missing, missing)
	}
}
