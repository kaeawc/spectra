package idle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// Report is the `spectra idle --json` document; the MCP idle tool returns
// the same value.
type Report struct {
	Roots         []string `json:"roots,omitempty"`
	PIDs          []int    `json:"pids,omitempty"`
	MinAgeSeconds int64    `json:"min_age_seconds"`
	MaxCPUPct     float64  `json:"max_cpu_pct"`
	WindowMS      int64    `json:"window_ms"`
	CPUBasis      string   `json:"cpu_basis"`
	Incomplete    bool     `json:"incomplete,omitempty"`
	Hint          string   `json:"hint,omitempty"`
	Result        Result   `json:"result"`
	Limitations   []string `json:"limitations"`
}

// Limitations is the caveat list every report carries.
var Limitations = []string{
	"Evidence only: nothing is signalled. A process that is legitimately waiting (a server between requests, an editor, a shell) also looks idle.",
	"CPU is summed over the process and all its descendants for one window; a descendant started during the window counts as activity.",
	"A process whose CPU could not be measured, or whose working directory could not be read, is never reported.",
	"Spectra, its ancestors and its descendants are excluded.",
}

// ReportOptions are the idle scan's inputs (the CLI's flags).
type ReportOptions struct {
	// Roots are directories whose processes are checked; with no PIDs
	// either, empty means the current directory (deps.Getwd).
	Roots   []string
	PIDs    []int
	MinAge  time.Duration
	MaxCPU  float64
	Window  time.Duration
	Timeout time.Duration
}

// DefaultReportOptions are the CLI flag defaults.
func DefaultReportOptions() ReportOptions {
	return ReportOptions{MinAge: 10 * time.Minute, MaxCPU: 1, Window: 5 * time.Second, Timeout: time.Minute}
}

// MinWindow is the shortest CPU sample window accepted.
const MinWindow = 100 * time.Millisecond

// ErrInvalidOptions reports out-of-range ReportOptions.
var ErrInvalidOptions = errors.New("invalid idle options")

// Validate rejects the option values the CLI rejects as usage errors.
func (o ReportOptions) Validate() error {
	if o.MinAge < 0 || o.MaxCPU <= 0 || o.Window < MinWindow || o.Timeout <= o.Window {
		return fmt.Errorf("%w: need min_age >= 0, max_cpu > 0, window >= 100ms, timeout > window", ErrInvalidOptions)
	}
	for _, p := range o.PIDs {
		if p <= 0 {
			return fmt.Errorf("%w: invalid pid %d", ErrInvalidOptions, p)
		}
	}
	return nil
}

// ReportDeps are the seams BuildReport uses so tests need no real host.
type ReportDeps struct {
	Sample func(ctx context.Context, window time.Duration) (Sample, error)
	// Cwds returns working directories and whether some could not be read.
	Cwds  func(ctx context.Context, procs []process.Info) (map[int]string, bool, error)
	Waits func(ctx context.Context, pids []int) map[int]*Wait
	// Under matches a cwd against a root's spellings; nil is path-only.
	Under        func(cwd string, roots []string) bool
	Stat         func(string) (os.FileInfo, error)
	Now          func() time.Time
	Pid          func() int
	Getwd        func() (string, error)
	Abs          func(string) (string, error)
	EvalSymlinks func(string) (string, error)
}

// DefaultReportDeps reads the real host.
func DefaultReportDeps() ReportDeps { return HostReportDeps(nil) }

// HostReportDeps reads the real host, taking the first CPU reading from
// baseline (a hostload.Tracker's) when it has one old enough.
func HostReportDeps(baseline func(time.Duration) (hostload.Baseline, bool)) ReportDeps {
	osk := hostos.Resolve(hostos.Unknown)
	collect := func(ctx context.Context) []process.Info {
		return process.CollectAll(ctx, process.CollectOptions{
			OS: osk, Runner: proc.Default,
			DetailCollector: func([]process.Info) map[int]process.Details { return map[int]process.Details{} },
		})
	}
	sampler := HostSampler{OS: osk, Runner: proc.Default, Collect: collect, Now: time.Now, Sleep: hostload.SleepContext, Baseline: baseline}
	finder := owner.Finder{OS: osk}
	return ReportDeps{
		Sample:       sampler.Sample,
		Cwds:         finder.Cwds,
		Waits:        WaitReader{OS: osk}.Read,
		Under:        finder.NewCwdMatcher().Under,
		Stat:         os.Stat,
		Now:          time.Now,
		Pid:          os.Getpid,
		Getwd:        os.Getwd,
		Abs:          filepath.Abs,
		EvalSymlinks: filepath.EvalSymlinks,
	}
}

// BuildReport lists long-idle holders under opts. opts must already be
// valid. Nothing is signalled.
func BuildReport(ctx context.Context, opts ReportOptions, deps ReportDeps) (Report, error) {
	if len(opts.Roots) == 0 && len(opts.PIDs) == 0 {
		wd, err := deps.Getwd()
		if err != nil {
			return Report{}, fmt.Errorf("current directory: %w", err)
		}
		opts.Roots = []string{wd}
	}
	if err := checkRoots(opts.Roots, deps); err != nil {
		return Report{}, err
	}
	return collect(ctx, opts, deps)
}

// checkRoots rejects a root that is not an existing directory: matching
// nothing under it would read as "no idle holders".
func checkRoots(roots []string, deps ReportDeps) error {
	if deps.Stat == nil {
		return nil
	}
	for _, r := range roots {
		fi, err := deps.Stat(r)
		if err != nil {
			return fmt.Errorf("--root %s: %w", r, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("--root %s is not a directory", r)
		}
	}
	return nil
}

func collect(ctx context.Context, opts ReportOptions, deps ReportDeps) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	s, err := deps.Sample(ctx, opts.Window)
	if err != nil {
		return Report{}, err
	}
	if s.Basis != BasisWindow || s.Window <= 0 {
		return Report{}, fmt.Errorf("CPU could not be measured over a window (basis %q), so idleness is unknown", s.Basis)
	}
	if len(s.Procs) == 0 {
		return Report{}, errors.New("the process list is empty (ps or /proc could not be read)")
	}
	rep := Report{
		Roots: opts.Roots, PIDs: opts.PIDs, MinAgeSeconds: int64(opts.MinAge / time.Second), MaxCPUPct: opts.MaxCPU,
		WindowMS: s.Window.Milliseconds(), CPUBasis: s.Basis, Limitations: Limitations,
	}
	var cwds map[int]string
	if len(opts.Roots) > 0 {
		var denied bool
		cwds, denied, err = deps.Cwds(ctx, s.Procs)
		if err != nil {
			return Report{}, err
		}
		if denied {
			rep.Incomplete = true
			rep.Hint = "some processes' working directories could not be read (other users); re-run with sudo"
		}
	}
	rep.Result = Find(s, cwds, Options{
		Roots: resolveRoots(opts.Roots, deps), PIDs: opts.PIDs, MinAge: opts.MinAge, MaxCPUPct: opts.MaxCPU,
		Now: deps.Now(), SelfPID: deps.Pid(), Under: deps.Under,
	})
	if len(rep.Result.Holders) > 0 {
		rep.Result.AttachWaits(deps.Waits(ctx, rep.Result.Pids()))
	}
	if err := ctx.Err(); err != nil {
		return Report{}, fmt.Errorf("gave up after --timeout %s: %w", opts.Timeout, err)
	}
	return rep, nil
}

func resolveRoots(roots []string, deps ReportDeps) []Root {
	var out []Root
	for _, r := range hostload.ResolveRoots(roots, deps.Abs, deps.EvalSymlinks) {
		path := r.Path
		if a, err := deps.Abs(path); err == nil {
			path = filepath.Clean(a)
		}
		out = append(out, Root{Path: path, Resolved: r.Resolved})
	}
	return out
}
