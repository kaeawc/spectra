package hostload

import (
	"context"
	"errors"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const gb = uint64(1 << 30)

func TestDecide(t *testing.T) {
	th := Thresholds{MaxLoadPerCPU: 1.5, MinFreeGB: 8, BackoffFreeGB: 4}
	tests := []struct {
		name string
		r    Reading
		want Verdict
		has  string
	}{
		{"admit", Reading{Load1: 3, CPUs: 8, AvailBytes: 20 * gb, MemKnown: true}, Admit, "20.0 GiB"},
		{"load exactly at limit holds", Reading{Load1: 12, CPUs: 8, AvailBytes: 20 * gb, MemKnown: true}, Hold, "12.00"},
		{"load just under limit admits", Reading{Load1: 11.99, CPUs: 8, AvailBytes: 20 * gb, MemKnown: true}, Admit, ""},
		{"memory exactly at min passes", Reading{Load1: 1, CPUs: 8, AvailBytes: 8 * gb, MemKnown: true}, Admit, ""},
		{"memory below min holds", Reading{Load1: 1, CPUs: 8, AvailBytes: 8*gb - 1<<20, MemKnown: true}, Hold, "minimum 8.0"},
		{"memory exactly at backoff holds not backoff", Reading{Load1: 1, CPUs: 8, AvailBytes: 4 * gb, MemKnown: true}, Hold, "4.0 GiB is below the minimum"},
		{"memory below backoff", Reading{Load1: 1, CPUs: 8, AvailBytes: 3 * gb, MemKnown: true}, Backoff, "backoff threshold 4.0"},
		{"backoff beats hold", Reading{Load1: 99, CPUs: 8, AvailBytes: 1 * gb, MemKnown: true}, Backoff, "1.0 GiB"},
		{"unknown memory fails closed", Reading{Load1: 1, CPUs: 8}, Hold, "could not be read"},
		{"unknown memory still holds on load", Reading{Load1: 50, CPUs: 8}, Hold, "50.00"},
		{"zero cpus treated as one", Reading{Load1: 1.5, CPUs: 0, AvailBytes: 20 * gb, MemKnown: true}, Hold, "1 CPUs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.r, th)
			if d.Verdict != tc.want {
				t.Fatalf("verdict = %s (%s), want %s", d.Verdict, d.Reason, tc.want)
			}
			if tc.has != "" && !contains(d.Reason, tc.has) {
				t.Fatalf("reason %q lacks %q", d.Reason, tc.has)
			}
		})
	}
	if Hold.ExitCode() != 3 || Backoff.ExitCode() != 4 || Admit.ExitCode() != 0 {
		t.Fatal("exit codes")
	}
}

func TestDecideRoundsLimit(t *testing.T) {
	// 1.1 x 3 is 3.3000000000000003 in float64; a load of 3.30 must still hold.
	th := Thresholds{MaxLoadPerCPU: 1.1, MinFreeGB: 1, BackoffFreeGB: 0}
	r := Reading{Load1: 3.30, CPUs: 3, AvailBytes: 10 * gb, MemKnown: true}
	if d := Decide(r, th); d.Verdict != Hold || d.LoadLimit != 3.3 {
		t.Fatalf("decision = %+v", d)
	}
	r.Load1 = 3.29
	if d := Decide(r, th); d.Verdict != Admit {
		t.Fatalf("decision = %+v", d)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParsers(t *testing.T) {
	if v, err := ParseProcLoadavg("0.52 0.58 0.59 1/467 12345\n"); err != nil || v != 0.52 {
		t.Fatalf("proc loadavg = %v %v", v, err)
	}
	if v, err := ParseSysctlLoadavg("{ 1.23 4.56 7.89 }\n"); err != nil || v != 1.23 {
		t.Fatalf("sysctl loadavg = %v %v", v, err)
	}
	if _, err := ParseSysctlLoadavg("  "); err == nil {
		t.Fatal("empty loadavg should error")
	}
	mi := "MemTotal:       16000000 kB\nMemFree:          100000 kB\nMemAvailable:    2048000 kB\n"
	if v, err := ParseMemAvailable(mi); err != nil || v != 2048000*1024 {
		t.Fatalf("meminfo = %v %v", v, err)
	}
	if _, err := ParseMemAvailable("MemTotal: 1 kB\n"); err == nil {
		t.Fatal("missing MemAvailable should error")
	}
	vm := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free:                               100.\nPages active:                             500.\nPages inactive:                           200.\nPages speculative:                         50.\n"
	if v, err := ParseVMStatAvailable(vm); err != nil || v != 350*16384 {
		t.Fatalf("vm_stat = %v %v", v, err)
	}
	if _, err := ParseVMStatAvailable("Pages free: 1.\n"); err == nil {
		t.Fatal("missing page size should error")
	}
}

func TestParseLsofCwd(t *testing.T) {
	got := ParseLsofCwd("p100\nn/Users/a/repo\np200\nn/\np300\np400\nn/tmp/x y\nnstray\n")
	want := map[int]string{100: "/Users/a/repo", 200: "/", 400: "/tmp/x y"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("pid %d = %q, want %q", k, got[k], v)
		}
	}
}

func TestHostDarwinCollect(t *testing.T) {
	f := proc.NewFake()
	f.OnExact("sysctl", []string{"-n", "vm.loadavg"}, proc.Response{Result: proc.Result{Stdout: []byte("{ 2.50 1 1 }\n")}})
	f.OnExact("vm_stat", nil, proc.Response{Result: proc.Result{Stdout: []byte("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 10.\nPages inactive: 5.\nPages speculative: 1.\n")}})
	// exit status 1 with partial stdout is the normal lsof outcome.
	f.OnExact("lsof", []string{"-a", "-d", "cwd", "-Fpn"}, proc.Response{Result: proc.Result{ExitCode: 1, Stdout: []byte("p7\nn/work/a\n")}})
	h := Host{OS: hostos.Darwin, Runner: f, NumCPU: func() int { return 4 }}
	ctx := context.Background()
	if v, err := h.Load1(ctx); err != nil || v != 2.5 {
		t.Fatalf("load = %v %v", v, err)
	}
	if v, err := h.Available(ctx); err != nil || v != 16*4096 {
		t.Fatalf("avail = %v %v", v, err)
	}
	cwds := h.Cwds(ctx, []int{7, 8})
	if cwds[7] != "/work/a" || len(cwds) != 1 {
		t.Fatalf("cwds = %v", cwds)
	}
	if h.CPUs() != 4 {
		t.Fatal("cpus")
	}
}

func TestHostLinuxCollect(t *testing.T) {
	files := map[string]string{
		"/proc/loadavg": "3.10 2 1 1/1 1\n",
		"/proc/meminfo": "MemAvailable: 1024 kB\n",
	}
	h := Host{
		OS: hostos.Linux,
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, errors.New("missing")
		},
		Readlink: func(p string) (string, error) {
			if p == "/proc/5/cwd" {
				return "/srv/app", nil
			}
			return "", errors.New("denied")
		},
	}
	ctx := context.Background()
	if v, err := h.Load1(ctx); err != nil || v != 3.1 {
		t.Fatalf("load = %v %v", v, err)
	}
	if v, err := h.Available(ctx); err != nil || v != 1024*1024 {
		t.Fatalf("avail = %v %v", v, err)
	}
	if c := h.Cwds(ctx, []int{5, 6}); c[5] != "/srv/app" || len(c) != 1 {
		t.Fatalf("cwds = %v", c)
	}
	delete(files, "/proc/meminfo")
	if _, err := h.Available(ctx); err == nil {
		t.Fatal("expected error")
	}
}

func fakeFS(paths ...string) FS {
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	return FS{Exists: func(p string) bool { return set[p] }}
}

func pi(pid int, cmd string, cpu float64, rssKiB int64) process.Info {
	return process.Info{PID: pid, Command: cmd, CPUPct: cpu, RSSKiB: rssKiB}
}

func TestAnalyzeRollup(t *testing.T) {
	fs := fakeFS(
		"/r/main/.git",             // directory
		"/r/main/wt/feat/.git",     // file (linked worktree), nested inside main
		"/r/main/wt/feat/sub/.git", // nested repo closer still
	)
	procs := []process.Info{
		pi(1, "go", 50, 100),
		pi(2, "node", 30, 200),
		pi(3, "javac", 20, 300),
		pi(4, "vim", 5, 50),
		pi(5, "daemon", 1, 10),
		pi(6, "root-owned", 1, 10),
	}
	cwds := map[int]string{
		1: "/r/main/wt/feat/a/b", // -> feat
		2: "/r/main/src",         // -> main
		3: "/r/main/wt/feat/sub/x",
		4: "/home/u", // outside any repo -> own cwd
		5: "/home/u",
	}
	a := Analyze(procs, cwds, Options{TopN: 10, FS: fs})
	dirs := map[string]DirUsage{}
	for _, d := range a.Dirs {
		dirs[d.Dir] = d
	}
	if d := dirs["/r/main/wt/feat"]; d.CPUPct != 50 || d.Procs != 1 {
		t.Fatalf("feat = %+v", d)
	}
	if d := dirs["/r/main"]; d.CPUPct != 30 {
		t.Fatalf("main = %+v", d)
	}
	if d := dirs["/r/main/wt/feat/sub"]; d.CPUPct != 20 {
		t.Fatalf("sub = %+v", d)
	}
	if d := dirs["/home/u"]; d.CPUPct != 6 || d.Procs != 2 {
		t.Fatalf("home = %+v", d)
	}
	if a.Unknown.Procs != 1 || a.Unknown.Dir != UnknownDir {
		t.Fatalf("unknown = %+v", a.Unknown)
	}
	if a.Dirs[0].Dir != "/r/main/wt/feat" {
		t.Fatalf("dirs not sorted: %+v", a.Dirs)
	}
}

func TestAnalyzeMemoizesRollup(t *testing.T) {
	calls := 0
	fs := FS{Exists: func(string) bool { calls++; return false }}
	procs := []process.Info{pi(1, "a", 1, 1), pi(2, "b", 1, 1), pi(3, "c", 1, 1)}
	Analyze(procs, map[int]string{1: "/x/y", 2: "/x/y", 3: "/x/y"}, Options{FS: fs})
	if calls != 3 { // /x/y, /x, / once each
		t.Fatalf("exists calls = %d, want 3", calls)
	}
}

func TestAnalyzeTopN(t *testing.T) {
	procs := []process.Info{pi(1, "a", 10, 5), pi(2, "b", 30, 1), pi(3, "c", 20, 9), pi(4, "d", 20, 7)}
	a := Analyze(procs, nil, Options{TopN: 2})
	if len(a.TopCPU) != 2 || a.TopCPU[0].PID != 2 || a.TopCPU[1].PID != 3 {
		t.Fatalf("top cpu = %+v", a.TopCPU)
	}
	if len(a.TopRSS) != 2 || a.TopRSS[0].PID != 3 || a.TopRSS[1].PID != 4 {
		t.Fatalf("top rss = %+v", a.TopRSS)
	}
	if a.Unknown.Procs != 4 {
		t.Fatalf("unknown = %+v", a.Unknown)
	}
	if len(a.Families) == 0 {
		t.Fatal("no families")
	}
}

func TestAnalyzeCause(t *testing.T) {
	fs := fakeFS("/work/roots/wt1/.git", "/work/roots/wt2/.git")
	roots := []Root{{Path: "/work/roots", Resolved: "/work/roots"}}
	tests := []struct {
		name  string
		procs []process.Info
		cwds  map[int]string
		kind  string
		check func(Cause) bool
	}{
		{"indexing", []process.Info{pi(1, "mds_stores", 40, 1), pi(2, "mdworker_shared", 30, 1), pi(3, "go", 50, 1)}, nil, CauseIndexing,
			func(c Cause) bool { return c.CPUPct == 70 }},
		{"linux indexing", []process.Info{pi(1, "tracker-miner-fs-3", 60, 1), pi(2, "go", 20, 1)}, nil, CauseIndexing, nil},
		{"roots", []process.Info{pi(1, "go", 30, 1), pi(2, "node", 40, 1), pi(3, "other", 50, 1)},
			map[int]string{1: "/work/roots/wt1/pkg", 2: "/work/roots/wt1", 3: "/elsewhere"}, CauseRoots,
			func(c Cause) bool { return c.Root == "/work/roots" && c.Worktree == "wt1" && c.CPUPct == 70 }},
		{"other names process", []process.Info{pi(1, "chrome", 80, 1), pi(2, "go", 30, 1)},
			map[int]string{1: "/elsewhere", 2: "/work/roots/wt2"}, CauseOther,
			func(c Cause) bool { return contains(c.Detail, "chrome") }},
		{"non-root directory summed beats indexing", []process.Info{pi(1, "clang", 50, 1), pi(2, "clang", 50, 1), pi(3, "clang", 50, 1), pi(4, "clang", 50, 1), pi(5, "mds_stores", 60, 1)},
			map[int]string{1: "/src/app", 2: "/src/app", 3: "/src/app", 4: "/src/app"}, CauseOther,
			func(c Cause) bool {
				return c.CPUPct == 200 && contains(c.Detail, "/src/app") && contains(c.Detail, "clang")
			}},
		{"idle", []process.Info{pi(1, "a", 2, 1), pi(2, "b", 3, 1)}, nil, CauseNone, nil},
		{"empty", nil, nil, CauseNone, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := Analyze(tc.procs, tc.cwds, Options{TopN: 3, Roots: roots, FS: fs})
			if a.Cause.Kind != tc.kind {
				t.Fatalf("cause = %+v, want %s", a.Cause, tc.kind)
			}
			if tc.check != nil && !tc.check(a.Cause) {
				t.Fatalf("cause check failed: %+v", a.Cause)
			}
		})
	}
}

func TestRootMatchesResolvedSymlinkAndReportsTyped(t *testing.T) {
	roots := []Root{{Path: "/tmp/wts", Resolved: "/private/tmp/wts"}}
	a := Analyze([]process.Info{pi(1, "go", 50, 1)}, map[int]string{1: "/private/tmp/wts/a"}, Options{TopN: 3, Roots: roots})
	if a.Cause.Kind != CauseRoots || a.Cause.Root != "/tmp/wts" || a.Cause.Worktree != "a" {
		t.Fatalf("cause = %+v", a.Cause)
	}
}

func TestRootsDoNotMatchSiblingPrefix(t *testing.T) {
	if underRoot("/work/rootsX/a", "/work/roots") || !underRoot("/work/roots/a", "/work/roots") || !underRoot("/work/roots", "/work/roots") {
		t.Fatal("underRoot prefix handling")
	}
}
