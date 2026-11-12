// Package procdiag answers "why is this Linux process slow or stuck?" from
// /proc alone: it samples every thread twice, a short interval apart, and
// reasons about the deltas. A single snapshot cannot tell a leak from a
// steady state or a stall from a blink, so the two-sample delta is the point.
//
// It is the Linux counterpart of the macOS sample/hang workflow, but needs no
// external tool and no privilege for the caller's own processes. Reading
// another user's process degrades gracefully: unreadable files are reported
// in Report.Notes and the checks that need them are skipped.
package procdiag

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/pressure"
)

// clkTck is USER_HZ; it is 100 on effectively every Linux target. Reading it
// needs cgo, which the static Linux build disables (see process/proclinux.go).
const clkTck = 100

// Options configures Inspect. The zero value samples /proc for one second.
type Options struct {
	// ProcRoot is the procfs mount, "/proc" when empty.
	ProcRoot string
	// CgroupRoot is the cgroup v2 mount, "/sys/fs/cgroup" when empty.
	CgroupRoot string
	// Interval between the two samples; 1s when zero.
	Interval time.Duration
	// Arch selects the syscall-name table; runtime.GOARCH when empty.
	Arch string
	// Sleep waits between samples. Tests replace it to mutate fixtures.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now returns the sample timestamp. time.Now when nil.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	if o.CgroupRoot == "" {
		o.CgroupRoot = "/sys/fs/cgroup"
	}
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ErrNoProcess is returned when the pid does not exist or exits mid-sample.
var ErrNoProcess = errors.New("no such process")

// Thread is the second-sample state of one thread plus its activity over the
// interval.
type Thread struct {
	TID     int    `json:"tid"`
	Comm    string `json:"comm"`
	State   string `json:"state"`
	WChan   string `json:"wchan,omitempty"`
	Syscall string `json:"syscall,omitempty"`
	// CPUPct is this thread's share of one core over the interval.
	CPUPct float64 `json:"cpu_pct"`
	// SystemPct is the part of CPUPct spent in the kernel.
	SystemPct float64 `json:"system_pct"`
	// CtxSwitches is voluntary+involuntary context switches over the
	// interval; zero for a thread that never ran or was woken.
	CtxSwitches int64 `json:"ctx_switches"`
}

// FDs is the descriptor table usage.
type FDs struct {
	Count      int     `json:"count"`
	Limit      int64   `json:"limit"`
	Delta      int     `json:"delta"`
	RatePerSec float64 `json:"rate_per_sec"`
}

// Memory is the process's memory in KiB.
type Memory struct {
	RSSKiB       int64   `json:"rss_kib"`
	AnonKiB      int64   `json:"anon_kib"`
	SwapKiB      int64   `json:"swap_kib"`
	PSSKiB       int64   `json:"pss_kib,omitempty"`
	RSSDeltaKiB  int64   `json:"rss_delta_kib"`
	RSSRateKiBPS float64 `json:"rss_rate_kib_per_sec"`
}

// IO is storage I/O (bytes that actually reached or came from the block
// layer, not page-cache hits) over the interval.
type IO struct {
	ReadBytes  int64 `json:"read_bytes"`
	WriteBytes int64 `json:"write_bytes"`
}

// Severity ranks a Finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Finding kinds.
const (
	KindZombie       = "zombie"
	KindStopped      = "stopped"
	KindUninterrupt  = "uninterruptible-io"
	KindLockWait     = "lock-wait"
	KindBlockedSys   = "blocked-syscall"
	KindCPUSpin      = "cpu-spin"
	KindFDExhaustion = "fd-exhaustion"
	KindFDGrowth     = "fd-growth"
	KindMemoryGrowth = "memory-growth"
	KindSwappedOut   = "swapped-out"
	KindActive       = "active"
	KindIdle         = "idle"
)

// Finding is one conclusion, with the raw observations that support it so a
// reader can check the reasoning instead of trusting the label.
type Finding struct {
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

// Report is the result of inspecting one process.
type Report struct {
	PID         int              `json:"pid"`
	Comm        string           `json:"comm"`
	Cmdline     string           `json:"cmdline,omitempty"`
	State       string           `json:"state"`
	IntervalSec float64          `json:"interval_sec"`
	CPUPct      float64          `json:"cpu_pct"`
	ThreadCount int              `json:"thread_count"`
	StateCounts map[string]int   `json:"state_counts"`
	Threads     []Thread         `json:"threads"`
	FDs         FDs              `json:"fds"`
	Memory      Memory           `json:"memory"`
	IO          *IO              `json:"io,omitempty"`
	OOMScore    int64            `json:"oom_score"`
	Cgroup      *pressure.Cgroup `json:"cgroup,omitempty"`
	Findings    []Finding        `json:"findings"`
	Verdict     string           `json:"verdict"`
	Notes       []string         `json:"notes,omitempty"`

	arch string
}

// maxReportedThreads bounds Report.Threads; a process with thousands of
// threads would otherwise produce an unreadable report. Threads are ranked
// by CPU, then by those in a non-sleeping state, so the interesting ones win.
const maxReportedThreads = 32

type taskSample struct {
	comm, state, wchan string
	syscallNr          int // -2 unreadable, -1 running/not in a syscall
	cpuTicks, sysTicks int64
	ctx                int64
	readable           bool
}

type procSample struct {
	at       time.Time
	tasks    map[int]taskSample
	fds      int
	rssKiB   int64
	ioRead   int64
	ioWrite  int64
	haveIO   bool
	notes    []string
	procName string
	state    string
}

// Inspect samples pid twice and returns the diagnosis. It returns
// ErrNoProcess (wrapped) when the process is gone.
func Inspect(ctx context.Context, pid int, opts Options) (Report, error) {
	opts = opts.withDefaults()
	first, err := takeSample(opts, pid)
	if err != nil {
		return Report{}, err
	}
	if err := opts.Sleep(ctx, opts.Interval); err != nil {
		return Report{}, fmt.Errorf("wait between samples: %w", err)
	}
	second, err := takeSample(opts, pid)
	if err != nil {
		return Report{}, err
	}
	return analyze(opts, pid, first, second), nil
}

func takeSample(opts Options, pid int) (procSample, error) {
	base := filepath.Join(opts.ProcRoot, strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return procSample{}, fmt.Errorf("pid %d: %w", pid, ErrNoProcess)
		}
		return procSample{}, fmt.Errorf("read stat of pid %d: %w", pid, err)
	}
	s := procSample{at: opts.Now(), tasks: map[int]taskSample{}}
	if st, ok := parseStat(stat); ok {
		s.procName, s.state = st.comm, st.state
	}

	entries, err := os.ReadDir(filepath.Join(base, "task"))
	if err != nil {
		return procSample{}, fmt.Errorf("list threads of pid %d: %w", pid, ErrNoProcess)
	}
	denied := map[string]bool{}
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		t, ok := readTask(filepath.Join(base, "task", e.Name()), denied)
		if ok {
			s.tasks[tid] = t
		}
	}
	for f := range denied {
		s.notes = append(s.notes, fmt.Sprintf("%s not readable (permission); checks that need it were skipped", f))
	}
	sort.Strings(s.notes)

	if ents, err := os.ReadDir(filepath.Join(base, "fd")); err == nil {
		s.fds = len(ents)
	} else {
		s.fds = -1
		s.notes = append(s.notes, "fd table not readable (permission)")
	}
	if st, err := os.ReadFile(filepath.Join(base, "status")); err == nil {
		s.rssKiB = statusKiB(st, "VmRSS")
	}
	if data, err := os.ReadFile(filepath.Join(base, "io")); err == nil {
		s.ioRead, s.ioWrite, s.haveIO = parseIO(data)
	}
	return s, nil
}

func readTask(dir string, denied map[string]bool) (taskSample, bool) {
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return taskSample{}, false // thread exited between listing and read
	}
	st, ok := parseStat(stat)
	if !ok {
		return taskSample{}, false
	}
	t := taskSample{comm: st.comm, state: st.state, cpuTicks: st.utime + st.stime, sysTicks: st.stime, syscallNr: -2, readable: true}
	if w, err := os.ReadFile(filepath.Join(dir, "wchan")); err == nil {
		if w := strings.TrimSpace(string(w)); w != "0" {
			t.wchan = w
		}
	} else if errors.Is(err, fs.ErrPermission) {
		denied["wchan"] = true
	}
	if sc, err := os.ReadFile(filepath.Join(dir, "syscall")); err == nil {
		t.syscallNr = parseSyscallNr(sc)
	} else if errors.Is(err, fs.ErrPermission) {
		denied["syscall"] = true
	}
	if status, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		t.ctx = statusInt(status, "voluntary_ctxt_switches") + statusInt(status, "nonvoluntary_ctxt_switches")
	}
	return t, true
}

type statFields struct {
	comm, state  string
	utime, stime int64
}

// parseStat parses /proc/<pid>/stat or /proc/<pid>/task/<tid>/stat. comm is
// parenthesised and may contain spaces and parentheses, so fields are taken
// from after the final ')'.
func parseStat(data []byte) (statFields, bool) {
	s := string(data)
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return statFields{}, false
	}
	f := strings.Fields(s[end+1:])
	// f[0] is state (field 3); utime and stime are fields 14 and 15.
	if len(f) < 13 {
		return statFields{}, false
	}
	ut, err1 := strconv.ParseInt(f[11], 10, 64)
	st, err2 := strconv.ParseInt(f[12], 10, 64)
	if err1 != nil || err2 != nil {
		return statFields{}, false
	}
	return statFields{comm: s[open+1 : end], state: f[0], utime: ut, stime: st}, true
}

// parseSyscallNr reads the first token of /proc/.../syscall: "running" when
// the thread is on a CPU, otherwise the syscall number (or -1 when blocked
// outside a syscall). It returns -1 for running and -2 when unparsable.
func parseSyscallNr(data []byte) int {
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return -2
	}
	if f[0] == "running" {
		return -1
	}
	n, err := strconv.Atoi(f[0])
	if err != nil {
		return -2
	}
	return n
}

func statusInt(data []byte, key string) int64 {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, key+":"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				v, _ := strconv.ParseInt(f[0], 10, 64)
				return v
			}
		}
	}
	return 0
}

func statusKiB(data []byte, key string) int64 { return statusInt(data, key) }

func parseIO(data []byte) (read, write int64, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "read_bytes":
			read, ok = n, true
		case "write_bytes":
			write = n
		}
	}
	return read, write, ok
}

// SyscallName resolves a raw syscall number for arch ("amd64" or "arm64");
// unknown numbers and architectures yield "syscall_<n>".
func SyscallName(arch string, nr int) string {
	var m map[int]string
	switch arch {
	case "amd64":
		m = syscallsAMD64
	case "arm64":
		m = syscallsARM64
	}
	if name, ok := m[nr]; ok {
		return name
	}
	return "syscall_" + strconv.Itoa(nr)
}

func analyze(opts Options, pid int, a, b procSample) Report {
	secs := b.at.Sub(a.at).Seconds()
	if secs <= 0 {
		secs = opts.Interval.Seconds()
	}
	r := Report{
		PID: pid, Comm: b.procName, State: b.state, IntervalSec: secs, arch: opts.Arch,
		StateCounts: map[string]int{}, ThreadCount: len(b.tasks), Notes: append([]string(nil), b.notes...),
	}
	r.Cmdline = readCmdline(opts.ProcRoot, pid)

	var totalTicks, totalSys int64
	var threads []Thread
	for tid, t := range b.tasks {
		r.StateCounts[t.state]++
		th := Thread{TID: tid, Comm: t.comm, State: t.state, WChan: t.wchan}
		switch {
		case t.syscallNr >= 0:
			th.Syscall = SyscallName(opts.Arch, t.syscallNr)
		case t.syscallNr == -1 && t.state == "R":
			th.Syscall = "running"
		}
		if prev, ok := a.tasks[tid]; ok {
			dt := max(t.cpuTicks-prev.cpuTicks, 0)
			ds := max(t.sysTicks-prev.sysTicks, 0)
			totalTicks += dt
			totalSys += ds
			th.CPUPct = pct(dt, secs)
			th.SystemPct = pct(ds, secs)
			th.CtxSwitches = max(t.ctx-prev.ctx, 0)
		}
		threads = append(threads, th)
	}
	r.CPUPct = pct(totalTicks, secs)
	r.Threads = rankThreads(threads)

	r.FDs = fdUsage(opts.ProcRoot, pid, a.fds, b.fds, secs)
	r.Memory = memoryUsage(opts.ProcRoot, pid, a.rssKiB, b.rssKiB, secs)
	if a.haveIO && b.haveIO {
		r.IO = &IO{ReadBytes: max(b.ioRead-a.ioRead, 0), WriteBytes: max(b.ioWrite-a.ioWrite, 0)}
	}
	r.OOMScore = readInt(filepath.Join(opts.ProcRoot, strconv.Itoa(pid), "oom_score"))

	if cg, err := pressure.ReadCgroup(opts.ProcRoot, opts.CgroupRoot, pid); err == nil {
		r.Cgroup = &cg
	} else if !errors.Is(err, pressure.ErrUnavailable) {
		r.Notes = append(r.Notes, "cgroup not readable: "+err.Error())
	}

	r.Findings = diagnose(&r, a, b, totalSys)
	r.Verdict = verdict(r.Findings, r.CPUPct)
	return r
}

func pct(ticks int64, secs float64) float64 {
	return 100 * float64(ticks) / clkTck / secs
}

func rankThreads(ts []Thread) []Thread {
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].CPUPct != ts[j].CPUPct {
			return ts[i].CPUPct > ts[j].CPUPct
		}
		bi, bj := ts[i].State != "S", ts[j].State != "S"
		if bi != bj {
			return bi
		}
		return ts[i].TID < ts[j].TID
	})
	if len(ts) > maxReportedThreads {
		ts = ts[:maxReportedThreads]
	}
	return ts
}

func fdUsage(procRoot string, pid, first, second int, secs float64) FDs {
	f := FDs{Count: max(second, 0)}
	if second < 0 || first < 0 {
		return f
	}
	f.Delta = second - first
	f.RatePerSec = float64(f.Delta) / secs
	if data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "limits")); err == nil {
		f.Limit = parseNoFileLimit(data)
	}
	return f
}

// parseNoFileLimit returns the soft "Max open files" limit, or 0 for
// "unlimited"/unparsable.
func parseNoFileLimit(data []byte) int64 {
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "Max open files"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				v, err := strconv.ParseInt(f[0], 10, 64)
				if err == nil {
					return v
				}
			}
		}
	}
	return 0
}

func memoryUsage(procRoot string, pid int, rss1, rss2 int64, secs float64) Memory {
	m := Memory{RSSKiB: rss2, RSSDeltaKiB: rss2 - rss1}
	m.RSSRateKiBPS = float64(m.RSSDeltaKiB) / secs
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	if st, err := os.ReadFile(filepath.Join(base, "status")); err == nil {
		m.AnonKiB = statusKiB(st, "RssAnon")
		m.SwapKiB = statusKiB(st, "VmSwap")
	}
	if sm, err := os.ReadFile(filepath.Join(base, "smaps_rollup")); err == nil {
		m.PSSKiB = statusKiB(sm, "Pss")
	}
	return m
}

func readInt(file string) int64 {
	data, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return v
}

func readCmdline(procRoot string, pid int) string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " "))
}
