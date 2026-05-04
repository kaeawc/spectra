package snapshot

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// Kind distinguishes a live snapshot from an immutable baseline.
// Today only Live is produced; baselines arrive with the daemon.
type Kind string

const (
	KindLive     Kind = "live"
	KindBaseline Kind = "baseline"
)

// Snapshot is the structured capture of one host at one point in time.
type Snapshot struct {
	ID         string               `json:"id"`
	TakenAt    time.Time            `json:"taken_at"`
	Kind       Kind                 `json:"kind"`
	Host       HostInfo             `json:"host"`
	Apps       []detect.Result      `json:"apps"`
	Processes  []process.Info       `json:"processes,omitempty"`
	Toolchains toolchain.Toolchains `json:"toolchains"`
	Power      sysinfo.PowerState   `json:"power"`
	Sysctls    map[string]string    `json:"sysctls,omitempty"`

	// Placeholders for upcoming collectors. Empty until implemented.
	// See docs/design/system-inventory.md.
	// JVMs    []JVMInfo
	// Network *NetworkState
	// Storage *StorageState
}

// Options configure a snapshot Build.
type Options struct {
	// SpectraVersion is recorded on HostInfo.
	SpectraVersion string

	// AppPaths are the .app bundles to include. When empty, Build scans
	// /Applications and /Applications/Utilities.
	AppPaths []string

	// DetectOpts are forwarded to each Detect call.
	DetectOpts detect.Options

	// ToolchainOpts are forwarded to the toolchain collector.
	// Zero value uses production defaults (live machine paths).
	ToolchainOpts toolchain.CollectOptions

	// ProcessOpts are forwarded to the process collector.
	// Zero value uses the real ps command.
	ProcessOpts process.CollectOptions

	// SkipProcesses disables the process collector (faster for tests).
	SkipProcesses bool

	// SysinfoCmdRunner is forwarded to sysinfo collectors (sysctls + power).
	// Zero value uses the real commands.
	SysinfoCmdRunner sysinfo.CmdRunner
}

// Build assembles a Snapshot by running every collector in parallel and
// composing their results. Any collector failure is silently absorbed
// per the system-inventory contract — partial snapshots are valid.
func Build(ctx context.Context, opts Options) Snapshot {
	s := Snapshot{
		ID:      newID(),
		TakenAt: time.Now().UTC(),
		Kind:    KindLive,
	}

	siRun := opts.SysinfoCmdRunner
	if siRun == nil {
		siRun = sysinfo.DefaultRunner
	}

	var wg sync.WaitGroup
	collectors := 5 // host, apps, toolchains, power, sysctls
	if !opts.SkipProcesses {
		collectors = 6
	}
	wg.Add(collectors)

	go func() {
		defer wg.Done()
		s.Host = CollectHost(opts.SpectraVersion)
	}()
	go func() {
		defer wg.Done()
		s.Apps = collectApps(ctx, opts)
	}()
	go func() {
		defer wg.Done()
		s.Toolchains = toolchain.Collect(ctx, opts.ToolchainOpts)
	}()
	go func() {
		defer wg.Done()
		s.Power = sysinfo.CollectPower(siRun)
	}()
	go func() {
		defer wg.Done()
		s.Sysctls = sysinfo.CollectSysctls(siRun)
	}()

	if !opts.SkipProcesses {
		go func() {
			defer wg.Done()
			s.Processes = process.CollectAll(ctx, opts.ProcessOpts)
		}()
	}

	wg.Wait()
	return s
}

// collectApps runs Detect across opts.AppPaths in parallel. When
// AppPaths is empty, it auto-discovers under /Applications.
func collectApps(_ context.Context, opts Options) []detect.Result {
	paths := opts.AppPaths
	if len(paths) == 0 {
		paths = append(paths, scanApps("/Applications")...)
		paths = append(paths, scanApps("/Applications/Utilities")...)
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)

	type job struct {
		i   int
		res detect.Result
		err error
	}
	workers := runtime.GOMAXPROCS(0)
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}
	in := make(chan int, len(paths))
	out := make(chan job, len(paths))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range in {
				r, err := detect.DetectWith(paths[i], opts.DetectOpts)
				out <- job{i: i, res: r, err: err}
			}
		}()
	}
	for i := range paths {
		in <- i
	}
	close(in)
	go func() { wg.Wait(); close(out) }()

	results := make([]detect.Result, len(paths))
	ok := make([]bool, len(paths))
	for j := range out {
		if j.err != nil {
			continue
		}
		results[j.i] = j.res
		ok[j.i] = true
	}
	final := make([]detect.Result, 0, len(paths))
	for i, good := range ok {
		if good {
			final = append(final, results[i])
		}
	}
	return final
}

// scanApps lists .app bundles directly under dir.
func scanApps(dir string) []string {
	entries, err := readDirSafe(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, name := range entries {
		if strings.HasSuffix(name, ".app") {
			out = append(out, filepath.Join(dir, name))
		}
	}
	return out
}

// newID returns a snapshot identifier of the form
// "snap-YYYYMMDDTHHMMSSZ-<short>". Stable across machines (UTC); short
// suffix avoids collision when multiple snapshots run in the same second.
func newID() string {
	now := time.Now().UTC()
	short := now.Format("150405.000000")
	short = strings.ReplaceAll(short, ".", "")
	return "snap-" + now.Format("20060102T150405Z") + "-" + short[len(short)-4:]
}
