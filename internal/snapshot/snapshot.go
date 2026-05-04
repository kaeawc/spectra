package snapshot

import (
	"context"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/storagestate"
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
	Power      sysinfo.PowerState    `json:"power"`
	Sysctls    map[string]string     `json:"sysctls,omitempty"`
	Network    netstate.State        `json:"network"`
	Storage    storagestate.State    `json:"storage"`

	JVMs []jvm.Info `json:"jvms,omitempty"`
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

	// SkipStorage disables the storage state collector (faster for tests;
	// walking ~/Library can take seconds on a full machine).
	SkipStorage bool

	// SysinfoCmdRunner is forwarded to sysinfo collectors (sysctls + power).
	// Zero value uses the real commands.
	SysinfoCmdRunner sysinfo.CmdRunner

	// NetCmdRunner is forwarded to the network state collector.
	// Zero value uses the real commands.
	NetCmdRunner netstate.CmdRunner

	// StorageOpts are forwarded to the storage state collector.
	// Zero value uses live filesystem paths.
	StorageOpts storagestate.CollectOptions

	// JVMOpts are forwarded to the JVM collector.
	// Zero value uses the real jps/jcmd commands.
	JVMOpts jvm.CollectOptions

	// SkipJVMs disables JVM process discovery (faster for tests; requires jps
	// in PATH for real collection).
	SkipJVMs bool

	// SkipApps disables the per-app Detect() pass entirely. Useful for
	// host-only snapshots where app data is not needed (e.g. daemon
	// periodic captures).
	SkipApps bool

	// DetectStore is the sharded cache for detect.Result JSON. When non-nil,
	// collectApps serves cached results keyed by Info.plist + main-exe hash and
	// stores new results on miss.
	DetectStore *cache.ShardedStore
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
	netRun := opts.NetCmdRunner
	if netRun == nil {
		netRun = netstate.DefaultRunner
	}

	var wg sync.WaitGroup
	collectors := 5 // host, toolchains, power, sysctls, network
	if !opts.SkipApps {
		collectors++
	}
	if !opts.SkipProcesses {
		collectors++
	}
	if !opts.SkipStorage {
		collectors++
	}
	if !opts.SkipJVMs {
		collectors++
	}
	wg.Add(collectors)

	go func() {
		defer wg.Done()
		s.Host = CollectHost(opts.SpectraVersion)
	}()
	if !opts.SkipApps {
		go func() {
			defer wg.Done()
			s.Apps = collectApps(ctx, opts)
		}()
	}
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
	go func() {
		defer wg.Done()
		s.Network = netstate.Collect(netRun)
	}()
	if !opts.SkipStorage {
		go func() {
			defer wg.Done()
			s.Storage = storagestate.Collect(opts.StorageOpts)
		}()
	}

	if !opts.SkipProcesses {
		go func() {
			defer wg.Done()
			s.Processes = process.CollectAll(ctx, opts.ProcessOpts)
		}()
	}
	if !opts.SkipJVMs {
		go func() {
			defer wg.Done()
			s.JVMs = jvm.CollectAll(ctx, opts.JVMOpts)
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
				r, err := detectWithCache(paths[i], opts.DetectOpts, opts.DetectStore)
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
