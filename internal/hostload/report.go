package hostload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/diskusage"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/storagestate"
)

// Report is the `spectra load --json` document; the MCP load tool returns
// the same value.
type Report struct {
	Verdict   string      `json:"verdict"`
	Reason    string      `json:"reason"`
	ExitCode  int         `json:"exit_code"`
	Load1     float64     `json:"load1"`
	LoadLimit float64     `json:"load_limit"`
	CPUs      int         `json:"cpus"`
	AvailGB   *float64    `json:"available_gb"`    // GiB; null when unreadable
	MinFreeGB float64     `json:"min_free_gb"`     // GiB
	BackoffGB float64     `json:"backoff_free_gb"` // GiB
	Cause     Cause       `json:"cause"`
	TopCPU    []ProcRow   `json:"top_cpu"`
	TopRSS    []ProcRow   `json:"top_rss"`
	Families  []FamilyRow `json:"families"`
	Dirs      []DirUsage  `json:"dirs"`
	Unknown   DirUsage    `json:"unknown"`
	Devices   Devices     `json:"devices"`
	// Spotlight is per root, present only when the cause is indexing.
	Spotlight   []RootSpotlight `json:"spotlight,omitempty"`
	Notes       []string        `json:"notes,omitempty"`
	CPUWindowMS int64           `json:"cpu_window_ms"` // >0 only for basis "window"
	CPUBasis    string          `json:"cpu_basis"`     // window, recent or lifetime
	ElapsedMS   int64           `json:"elapsed_ms"`
	ProcessCnt  int             `json:"process_count"`
}

// ReportOptions are the load check's inputs (the CLI's flags).
type ReportOptions struct {
	Top           int
	Roots         []string
	MaxLoadPerCPU float64
	MinFreeGB     float64
	BackoffFreeGB float64
	// Sample is the CPU measurement window; 0 uses lifetime averages.
	Sample     time.Duration
	BootWindow time.Duration
}

// DefaultReportOptions are the CLI flag defaults.
func DefaultReportOptions() ReportOptions {
	return ReportOptions{
		Top: 5, MaxLoadPerCPU: 1.5, MinFreeGB: 8, BackoffFreeGB: 4,
		Sample: 500 * time.Millisecond, BootWindow: DefaultBootWindow,
	}
}

// ErrInvalidOptions reports out-of-range ReportOptions.
var ErrInvalidOptions = errors.New("invalid load options")

// Validate rejects the option values the CLI rejects as usage errors.
func (o ReportOptions) Validate() error {
	if o.Top < 1 || o.MaxLoadPerCPU <= 0 || o.MinFreeGB < 0 || o.BackoffFreeGB < 0 || o.Sample < 0 || o.BootWindow <= 0 {
		return fmt.Errorf("%w: need top >= 1, max_load_per_cpu > 0, min_free_gb and backoff_free_gb >= 0, sample >= 0, boot_window > 0", ErrInvalidOptions)
	}
	return nil
}

// ReportDeps are the seams BuildReport uses so tests need no real host.
type ReportDeps struct {
	Host  Host
	Procs func(ctx context.Context) []process.Info
	FS    FS
	Now   func() time.Time
	// Sleep waits between the two CPU readings; nil disables sampling.
	Sleep func(ctx context.Context, d time.Duration)
	// Abs makes roots absolute (cwds are absolute); nil leaves them as typed.
	Abs func(string) (string, error)
	// EvalSymlinks resolves roots; nil means no resolution.
	EvalSymlinks func(string) (string, error)
	// Spotlight reads per-root Spotlight state when indexing is the cause;
	// its OS defaults to Host.OS.
	Spotlight SpotlightProbe
}

// DefaultReportDeps reads the real host.
func DefaultReportDeps() ReportDeps {
	host := Host{OS: hostos.Resolve(hostos.Unknown), Runner: proc.OS{}}
	return ReportDeps{
		Host: host,
		Procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{
				Runner: host.Runner,
				OS:     host.OS,
				// A non-nil collector that returns nothing skips per-process
				// libproc work on macOS; ps already supplies what we need.
				DetailCollector: func([]process.Info) map[int]process.Details { return map[int]process.Details{} },
			})
		},
		FS:           FS{Exists: lexists},
		Now:          time.Now,
		Sleep:        SleepContext,
		Abs:          filepath.Abs,
		EvalSymlinks: filepath.EvalSymlinks,
		Spotlight:    defaultSpotlightProbe(host),
	}
}

func lexists(p string) bool { _, err := os.Lstat(p); return err == nil }

// SleepContext waits d or until ctx is done.
func SleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// rootSizeBound caps the walk of each root when sizing it for the indexing
// report, so a huge worktree cannot stall the load check.
const rootSizeBound = 3 * time.Second

func defaultSpotlightProbe(host Host) SpotlightProbe {
	run := func(ctx context.Context) func(name string, args ...string) ([]byte, error) {
		return func(name string, args ...string) ([]byte, error) {
			return proc.Output(ctx, host.Runner, name, args...)
		}
	}
	return SpotlightProbe{
		OS:       host.OS,
		Exists:   lexists,
		ReadFile: os.ReadFile,
		Volume: func(ctx context.Context, p string) (storagestate.SpotlightVolume, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return storagestate.SpotlightForPath(run(ctx), p)
		},
		VolumeKey: func(ctx context.Context, p string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return storagestate.MountPointForPath(run(ctx), p)
		},
		Size: func(ctx context.Context, p string) (RootSize, error) {
			ctx, cancel := context.WithTimeout(ctx, rootSizeBound)
			defer cancel()
			res, err := diskusage.Walk(ctx, p, diskusage.Options{Top: 1})
			if err != nil {
				return RootSize{}, fmt.Errorf("size %s: %w", p, err)
			}
			return RootSize{Files: res.FileCount, Bytes: res.TotalBytes, Partial: res.Partial}, nil
		},
	}
}

// ResolveRoots makes each non-empty root absolute (abs) and symlink-resolved
// (eval), keeping the spelling as given in Path.
func ResolveRoots(roots []string, abs, eval func(string) (string, error)) []Root {
	out := make([]Root, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		match := filepath.Clean(r)
		if abs != nil {
			if a, err := abs(r); err == nil {
				match = filepath.Clean(a)
			}
		}
		root := Root{Path: r, Resolved: match}
		if eval != nil {
			if res, err := eval(match); err == nil {
				root.Resolved = res
			}
		}
		out = append(out, root)
	}
	return out
}

// BuildReport answers "can this machine take another job, and if not, who
// is using it?". opts must already be valid.
func BuildReport(ctx context.Context, opts ReportOptions, deps ReportDeps) (Report, error) {
	start := deps.Now()
	load1, err := deps.Host.Load1(ctx)
	if err != nil {
		return Report{}, err
	}
	reading := Reading{Load1: load1, CPUs: deps.Host.CPUs()}
	var notes []string
	if avail, err := deps.Host.Available(ctx); err != nil {
		notes = append(notes, fmt.Sprintf("available memory unknown: %v", err))
	} else {
		reading.AvailBytes, reading.MemKnown = avail, true
	}
	dec := Decide(reading, Thresholds{MaxLoadPerCPU: opts.MaxLoadPerCPU, MinFreeGB: opts.MinFreeGB, BackoffFreeGB: opts.BackoffFreeGB})

	sampler := Sampler{Host: deps.Host, Collect: deps.Procs, Now: deps.Now, Sleep: deps.Sleep}
	procs, window, basis := sampler.Sample(ctx, opts.Sample)
	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}
	rootList := ResolveRoots(opts.Roots, deps.Abs, deps.EvalSymlinks)
	an := Analyze(procs, deps.Host.Cwds(ctx, pids), Options{TopN: opts.Top, Roots: rootList, FS: deps.FS, Now: deps.Now(), BootWindow: opts.BootWindow})

	rep := Report{
		Verdict: string(dec.Verdict), Reason: dec.Reason, ExitCode: dec.Verdict.ExitCode(),
		Load1: reading.Load1, LoadLimit: dec.LoadLimit, CPUs: reading.CPUs,
		MinFreeGB: opts.MinFreeGB, BackoffGB: opts.BackoffFreeGB,
		Cause: an.Cause, TopCPU: an.TopCPU, TopRSS: an.TopRSS, Families: an.Families,
		Dirs: an.Dirs, Unknown: an.Unknown, Devices: an.Devices, Notes: notes,
		CPUWindowMS: window.Milliseconds(), CPUBasis: string(basis),
		ElapsedMS: deps.Now().Sub(start).Milliseconds(), ProcessCnt: len(procs),
	}
	rep.Spotlight = rootSpotlight(ctx, deps, an.Cause, rootList)
	if reading.MemKnown {
		gb := float64(reading.AvailBytes) / (1 << 30)
		rep.AvailGB = &gb
	}
	return rep, nil
}

// rootSpotlight reports each root's Spotlight state, only when indexing is
// the cause and roots were named.
func rootSpotlight(ctx context.Context, deps ReportDeps, cause Cause, roots []Root) []RootSpotlight {
	if cause.Kind != CauseIndexing || len(roots) == 0 {
		return nil
	}
	probe := deps.Spotlight
	if probe.OS == hostos.Unknown {
		probe.OS = deps.Host.OS
	}
	return probe.Inspect(ctx, roots)
}
