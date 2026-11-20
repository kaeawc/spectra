package idle

import (
	"context"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

var now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

// fixture: zsh (10) runs spectra (99); under /work/lane a slot wrapper (20)
// runs a nested wrapper (21) waiting on the slot; (30) is a build whose
// child (31) is compiling.
func fixture() []process.Info {
	return []process.Info{
		{PID: 1, Command: "launchd"},
		{PID: 10, PPID: 1, Command: "zsh", StartTime: ago(time.Hour)},
		{PID: 99, PPID: 10, Command: "spectra", StartTime: ago(time.Second)},
		{PID: 20, PPID: 1, Command: "slot", User: "me", FullCommandLine: "slot run", StartTime: ago(30 * time.Minute)},
		{PID: 21, PPID: 20, Command: "slot", StartTime: ago(29 * time.Minute)},
		{PID: 30, PPID: 1, Command: "wrapper", StartTime: ago(30 * time.Minute)},
		{PID: 31, PPID: 30, Command: "javac", StartTime: ago(20 * time.Minute)},
		{PID: 40, PPID: 1, Command: "young", StartTime: ago(time.Minute)},
	}
}

func cwds() map[int]string {
	return map[int]string{
		10: "/work/lane", 99: "/work/lane", 20: "/work/lane", 21: "/work/lane/sub",
		30: "/private/work/lane/b", 31: "/tmp", 40: "/work/lane",
	}
}

func sample(cpu map[int]float64) Sample {
	return Sample{Procs: fixture(), CPU: cpu, Window: 5 * time.Second, Basis: BasisWindow}
}

func idleCPU() map[int]float64 {
	return map[int]float64{1: 3, 10: 0, 99: 50, 20: 0.1, 21: 0, 30: 0, 31: 0.2, 40: 0}
}

func opts() Options {
	return Options{
		Roots:  []Root{{Path: "/work/lane", Resolved: "/private/work/lane"}},
		MinAge: 10 * time.Minute, MaxCPUPct: 1, Now: now, SelfPID: 99,
	}
}

func pids(hs []Holder) []int {
	var out []int
	for _, h := range hs {
		out = append(out, h.PID)
	}
	return out
}

func TestFindIdleParentAndChild(t *testing.T) {
	res := Find(sample(idleCPU()), cwds(), opts())
	if got := pids(res.Holders); !reflect.DeepEqual(got, []int{20, 30}) {
		t.Fatalf("holders = %v, want [20 30]", got)
	}
	h := res.Holders[0]
	if h.Matched != "/work/lane" || h.Cwd != "/work/lane" || h.DescendantsTotal != 1 || h.Descendants[0].PID != 21 {
		t.Errorf("holder = %+v", h)
	}
	if h.AgeSeconds != 1800 || h.TreeCPUPct < 0.09 || h.TreeCPUPct > 0.11 || len(h.Chain) != 1 || h.Chain[0].PID != 1 {
		t.Errorf("holder = %+v", h)
	}
	// 21 is idle too but folded into 20; zsh and spectra are excluded; 40 is young.
	if res.Nested != 1 || res.Young != 1 || res.Busy != 0 {
		t.Errorf("counts = %+v", res)
	}
}

func TestFindBusyChildSuppresses(t *testing.T) {
	cpu := idleCPU()
	cpu[21] = 40
	cpu[31] = 95
	res := Find(sample(cpu), cwds(), opts())
	if len(res.Holders) != 0 {
		t.Fatalf("holders = %v, want none", pids(res.Holders))
	}
	if res.Busy != 3 { // 20 (via 21), 21 itself, 30 (via 31)
		t.Errorf("busy = %d", res.Busy)
	}
}

func TestFindTreeSumCounts(t *testing.T) {
	cpu := idleCPU()
	cpu[20], cpu[21] = 0.6, 0.6 // each under 1%, together over: only the leaf 21 is idle
	if got := pids(Find(sample(cpu), cwds(), opts()).Holders); !reflect.DeepEqual(got, []int{21, 30}) {
		t.Errorf("holders = %v", got)
	}
}

func TestFindChildSpawnedInWindowIsActivity(t *testing.T) {
	s := sample(idleCPU())
	s.Procs = append(s.Procs, process.Info{PID: 22, PPID: 20, Command: "sh", StartTime: ago(2 * time.Second)})
	s.CPU[22] = 0
	// 20 is spawning, so it is active; its other child 21 is still idle.
	if got := pids(Find(s, cwds(), opts()).Holders); !reflect.DeepEqual(got, []int{21, 30}) {
		t.Errorf("holders = %v", got)
	}
}

func TestFindUnmeasured(t *testing.T) {
	cpu := idleCPU()
	delete(cpu, 21)
	res := Find(sample(cpu), cwds(), opts())
	if got := pids(res.Holders); !reflect.DeepEqual(got, []int{30}) || res.Unmeasured != 2 {
		t.Errorf("holders = %v, unmeasured = %d", got, res.Unmeasured)
	}
	s := sample(idleCPU())
	s.Basis = "lifetime"
	if res := Find(s, cwds(), opts()); len(res.Holders) != 0 {
		t.Errorf("a non-window basis must report nothing, got %v", pids(res.Holders))
	}
}

func TestFindByPID(t *testing.T) {
	o := opts()
	o.Roots, o.PIDs = nil, []int{31, 777}
	res := Find(sample(idleCPU()), cwds(), o)
	if got := pids(res.Holders); !reflect.DeepEqual(got, []int{31}) || res.Holders[0].Matched != "pid" {
		t.Errorf("holders = %+v", res.Holders)
	}
	if !reflect.DeepEqual(res.MissingPIDs, []int{777}) {
		t.Errorf("missing = %v", res.MissingPIDs)
	}
	// Spectra's own ancestor is never a candidate, even by pid.
	o.PIDs = []int{10}
	if res := Find(sample(idleCPU()), cwds(), o); len(res.Holders) != 0 {
		t.Errorf("holders = %v", pids(res.Holders))
	}
}

func TestParseCPUTime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"0:00.09":     90 * time.Millisecond,
		"3:51.98":     3*time.Minute + 51980*time.Millisecond,
		"123:45.00":   123*time.Minute + 45*time.Second,
		"01:02:03":    time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:00:00":  49 * time.Hour,
		"10:00:00.50": 10*time.Hour + 500*time.Millisecond,
	} {
		got, ok := ParseCPUTime(in)
		if !ok || (got-want).Abs() > time.Millisecond {
			t.Errorf("%q = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "12", "a:b", "1:2:3:4", "x-1:00"} {
		if _, ok := ParseCPUTime(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSampleDarwin(t *testing.T) {
	calls := 0
	run := proc.NewFake().On(proc.MatchPrefix("ps", "-axo", "pid=,time="), proc.Response{Result: proc.Result{Stdout: []byte(" 20 0:01.00\n 21 0:02.00\n 50 0:00.00\n")}})
	clock := now
	h := HostSampler{
		OS: hostos.Darwin, Runner: run,
		Collect: func(context.Context) []process.Info {
			calls++
			return []process.Info{
				{PID: 20, StartTime: ago(time.Hour)},
				{PID: 21, StartTime: ago(time.Hour)},
				{PID: 60, StartTime: ago(time.Hour)}, // not in ps output
			}
		},
		Now:   func() time.Time { return clock },
		Sleep: func(_ context.Context, d time.Duration) { clock = clock.Add(d) },
	}
	s, err := h.Sample(context.Background(), 4*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if s.Basis != BasisWindow || s.Window != 4*time.Second || calls != 1 {
		t.Fatalf("sample = %+v calls %d", s, calls)
	}
	if !reflect.DeepEqual(s.CPU, map[int]float64{20: 0, 21: 0}) {
		t.Errorf("cpu = %v", s.CPU)
	}
}

func TestSampleLinuxUsesHostload(t *testing.T) {
	stat := func(cpu int) string {
		return "20 (slot) S 1 20 20 0 -1 0 0 0 0 0 " + strconv.Itoa(cpu) + " 0 0 0 20 0 1 0 100 0 0"
	}
	reads := 0
	clock := now
	h := HostSampler{
		OS: hostos.Linux,
		ReadFile: func(p string) ([]byte, error) {
			if p != "/proc/20/stat" {
				return nil, fs.ErrNotExist
			}
			reads++
			if reads == 1 {
				return []byte(stat(100)), nil
			}
			return []byte(stat(150)), nil
		},
		Collect: func(context.Context) []process.Info { return []process.Info{{PID: 20, StartTime: ago(time.Hour)}} },
		Now:     func() time.Time { return clock },
		Sleep:   func(_ context.Context, d time.Duration) { clock = clock.Add(d) },
	}
	s, err := h.Sample(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// 50 ticks at 100 Hz in 1s is 50%.
	if s.Basis != BasisWindow || s.CPU[20] < 49 || s.CPU[20] > 51 {
		t.Errorf("sample = %+v", s)
	}
}

type dent string

func (d dent) Name() string               { return string(d) }
func (d dent) IsDir() bool                { return false }
func (d dent) Type() fs.FileMode          { return 0 }
func (d dent) Info() (fs.FileInfo, error) { return nil, os.ErrNotExist }

func TestWaitLinux(t *testing.T) {
	files := map[string]string{"/proc/20/wchan": "futex_wait_queue\n", "/proc/21/wchan": "0"}
	links := map[string]string{"/proc/20/fd/3": "/work/lane/.slot.lock", "/proc/20/fd/4": "/work/lane/build.log", "/proc/21/fd/0": "pipe:[1]"}
	w := WaitReader{
		OS: hostos.Linux,
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		ReadDir: func(p string) ([]os.DirEntry, error) {
			switch p {
			case "/proc/20/fd":
				return []os.DirEntry{dent("3"), dent("4")}, nil
			case "/proc/21/fd":
				return []os.DirEntry{dent("0")}, nil
			}
			return nil, fs.ErrPermission
		},
		Readlink: func(p string) (string, error) {
			if s, ok := links[p]; ok {
				return s, nil
			}
			return "", fs.ErrNotExist
		},
	}
	got := w.Read(context.Background(), []int{20, 21})
	want := map[int]*Wait{20: {WChan: "futex_wait_queue", LockFiles: []string{"/work/lane/.slot.lock"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestWaitDarwin(t *testing.T) {
	out := "p20\nfcwd\nn/work/lane\nf5\nn/work/lane/slot.lock\nf6\nn/work/lane/slot.lock\np21\nf3\nn/usr/lib/libclock.dylib\n"
	run := proc.NewFake().On(proc.MatchExact("lsof", "-w", "-nP", "-p", "20,21", "-Fpn"), proc.Response{Result: proc.Result{ExitCode: 1, Stdout: []byte(out)}})
	got := WaitReader{OS: hostos.Darwin, Runner: run}.Read(context.Background(), []int{20, 21})
	want := map[int]*Wait{20: {LockFiles: []string{"/work/lane/slot.lock"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestIsLockPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/a/x.lock": true, "/a/LOCK": true, "/a/x.lck": true, "/a/.lock-foo": true, "/a/gc.lock.tmp": true,
		"/a/clock.so": false, "/a/locksmith": false, "x.lock": false, "/a/x.lock (deleted)": true,
	} {
		if got := IsLockPath(p); got != want {
			t.Errorf("IsLockPath(%q) = %v", p, got)
		}
	}
}
