// Package syslimits collects macOS kernel resource limits and current usage.
package syslimits

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// CmdRunner abstracts subprocess calls for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Options configures collection.
type Options struct {
	Run CmdRunner
	UID int
	Now func() time.Time
}

// SystemLimits is the kernel resource-limit snapshot.
type SystemLimits struct {
	PTY             ResourceUsage `json:"pty"`
	Files           ResourceUsage `json:"files"`
	FilesPerProc    int           `json:"max_files_per_proc"`
	Procs           ResourceUsage `json:"procs"`
	ProcsPerUID     ResourceUsage `json:"procs_per_uid"`
	CollectedAt     time.Time     `json:"collected_at"`
	PartialFailures []string      `json:"partial_failures,omitempty"`
}

// ResourceUsage is one current-vs-limit measurement.
type ResourceUsage struct {
	Limit    int     `json:"limit"`
	Current  int     `json:"current"`
	Pct      float64 `json:"pct"`
	Warn     bool    `json:"warn"`
	Critical bool    `json:"critical"`
}

// TopHolder is one process contributing to a saturated resource.
type TopHolder struct {
	PID     int    `json:"pid"`
	Count   int    `json:"count"`
	Command string `json:"command"`
}

// TopHolders groups per-resource process attribution.
type TopHolders map[string][]TopHolder

// Collect gathers resource usage with best-effort fallbacks.
func Collect(opts Options) SystemLimits {
	run := opts.Run
	if run == nil {
		run = DefaultRunner
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	uid := opts.UID
	if uid == 0 {
		uid = os.Getuid()
	}

	var failures []string
	intVal := func(key string) int {
		v, ok := sysctlInt(run, key)
		if !ok {
			failures = append(failures, key)
		}
		return v
	}

	ptyCurrent, ok := currentPTYs(run)
	if !ok {
		failures = append(failures, "lsof:pty")
	}
	procCurrent, ok := processCount(run, "-A")
	if !ok {
		failures = append(failures, "ps:procs")
	}
	procUIDCurrent, ok := processCount(run, "-u", strconv.Itoa(uid))
	if !ok {
		failures = append(failures, "ps:procs_per_uid")
	}

	return SystemLimits{
		PTY:             usage(ptyCurrent, intVal("kern.tty.ptmx_max")),
		Files:           usage(intVal("kern.num_files"), intVal("kern.maxfiles")),
		FilesPerProc:    intVal("kern.maxfilesperproc"),
		Procs:           usage(procCurrent, intVal("kern.maxproc")),
		ProcsPerUID:     usage(procUIDCurrent, intVal("kern.maxprocperuid")),
		CollectedAt:     now().UTC(),
		PartialFailures: failures,
	}
}

func sysctlInt(run CmdRunner, key string) (int, bool) {
	out, err := run("sysctl", "-n", key)
	if err != nil {
		return 0, false
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(out)))
	return v, err == nil
}

func currentPTYs(run CmdRunner) (int, bool) {
	out, err := run("lsof", "-n", "-P", "-d", "^txt")
	if err != nil && len(out) == 0 {
		return 0, false
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		fd := fields[3]
		if fd == "" || fd[0] < '0' || fd[0] > '9' {
			continue
		}
		if isPTYPath(strings.Join(fields[8:], " ")) {
			count++
		}
	}
	return count, true
}

func processCount(run CmdRunner, args ...string) (int, bool) {
	fullArgs := append([]string{}, args...)
	fullArgs = append(fullArgs, "-o", "pid=")
	out, err := run("ps", fullArgs...)
	if err != nil {
		return 0, false
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, true
}

func usage(current, limit int) ResourceUsage {
	u := ResourceUsage{Limit: limit, Current: current}
	if limit > 0 {
		u.Pct = (float64(current) / float64(limit)) * 100
	}
	u.Warn = u.Pct > 80
	u.Critical = u.Pct > 95
	return u
}

// AnyCritical reports whether any resource is above the critical threshold.
func (s SystemLimits) AnyCritical() bool {
	return s.PTY.Critical || s.Files.Critical || s.Procs.Critical || s.ProcsPerUID.Critical
}

// CollectTopHolders returns top process holders for saturated resources.
func CollectTopHolders(ctx context.Context, limit int, opts process.CollectOptions) TopHolders {
	if limit <= 0 {
		limit = 10
	}
	opts.Deep = true
	procs := process.CollectAll(ctx, opts)
	out := TopHolders{}
	out["pty"] = topBy(procs, limit, func(p process.Info) int {
		if p.FDBreakdown == nil {
			return 0
		}
		return p.FDBreakdown.PTY
	})
	out["files"] = topBy(procs, limit, func(p process.Info) int { return p.OpenFDs })
	out["procs"] = topByChildren(procs, limit, -1)
	out["procs_per_uid"] = topByChildren(procs, limit, os.Getuid())
	return out
}

func topBy(procs []process.Info, limit int, count func(process.Info) int) []TopHolder {
	rows := make([]TopHolder, 0, len(procs))
	for _, p := range procs {
		c := count(p)
		if c <= 0 {
			continue
		}
		rows = append(rows, TopHolder{PID: p.PID, Count: c, Command: p.Command})
	}
	sortTop(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func topByChildren(procs []process.Info, limit int, uid int) []TopHolder {
	parents := make(map[int]TopHolder)
	names := make(map[int]string)
	for _, p := range procs {
		names[p.PID] = p.Command
	}
	for _, p := range procs {
		if uid >= 0 && p.UID != uid {
			continue
		}
		if p.PPID <= 0 {
			continue
		}
		h := parents[p.PPID]
		h.PID = p.PPID
		h.Count++
		h.Command = names[p.PPID]
		parents[p.PPID] = h
	}
	rows := make([]TopHolder, 0, len(parents))
	for _, h := range parents {
		if h.Command == "" {
			h.Command = "pid " + strconv.Itoa(h.PID)
		}
		rows = append(rows, h)
	}
	sortTop(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func sortTop(rows []TopHolder) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && lessTop(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func lessTop(a, b TopHolder) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	return a.PID < b.PID
}

func isPTYPath(name string) bool {
	return strings.HasPrefix(name, "/dev/ttys") ||
		strings.HasPrefix(name, "/dev/pty") ||
		name == "/dev/ptmx"
}
