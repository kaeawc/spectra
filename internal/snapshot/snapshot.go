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
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/idgen"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/syslimits"
	"github.com/kaeawc/spectra/internal/telemetry"
	"github.com/kaeawc/spectra/internal/toolchain"
	"github.com/kaeawc/spectra/internal/updates"
)

// Kind distinguishes a live snapshot from an immutable baseline.
type Kind string

const (
	KindLive     Kind = "live"
	KindBaseline Kind = "baseline"
)

// Snapshot is the structured capture of one host at one point in time.
type Snapshot struct {
	ID           string                   `json:"id"`
	TakenAt      time.Time                `json:"taken_at"`
	Kind         Kind                     `json:"kind"`
	Tag          string                   `json:"tag,omitempty"`
	Host         HostInfo                 `json:"host"`
	HostFacts    HostFacts                `json:"host_facts"`
	Apps         []detect.Result          `json:"apps"`
	Processes    []process.Info           `json:"processes,omitempty"`
	Toolchains   toolchain.Toolchains     `json:"toolchains"`
	Power        sysinfo.PowerState       `json:"power"`
	SystemLimits syslimits.SystemLimits   `json:"system_limits"`
	Sysctls      map[string]string        `json:"sysctls,omitempty"`
	FDLimit      sysinfo.FDLimit          `json:"fd_limit,omitempty"`
	Network      netstate.State           `json:"network"`
	Storage      storagestate.State       `json:"storage"`
	Services     services.LaunchInventory `json:"services,omitempty"`
	Updates      updates.Result           `json:"updates,omitempty"`

	JVMs             []jvm.Info          `json:"jvms,omitempty"`
	RuntimeTelemetry []telemetry.Process `json:"runtime_telemetry,omitempty"`

	// OOMReports records java.lang.OutOfMemoryError occurrences found in the
	// discovered log files of running JVMs. Populated only in deep mode (when
	// process LogFiles are available); empty otherwise.
	OOMReports []OOMReport `json:"oom_reports,omitempty"`

	// JVMHistory is recent per-PID JVM samples (oldest first) populated by
	// callers that have access to a snapshot store. Optional: rules that
	// don't see history fall back to point-in-time checks.
	JVMHistory JVMHistory `json:"jvm_history,omitempty"`

	// FDHistory is recent per-PID file-descriptor samples (oldest first)
	// populated by callers that have access to a snapshot store. Optional:
	// rules that don't see history fall back to point-in-time checks.
	FDHistory FDHistory `json:"fd_history,omitempty"`

	// Warnings records collectors that could not run (e.g. a required tool was
	// missing), so a partial snapshot is not mistaken for a clean machine.
	// Empty when every requested collector produced trustworthy data.
	Warnings []string `json:"warnings,omitempty"`
}

// Options configure a snapshot Build.
type Options struct {
	// SpectraVersion is recorded on HostInfo.
	SpectraVersion string

	// Clock controls snapshot timestamps and default snapshot IDs.
	// Zero value uses the system clock.
	Clock clock.Clock

	// IDGenerator overrides snapshot ID generation. When nil, IDs retain the
	// historical snap-YYYYMMDDTHHMMSSZ-NNNN format derived from Clock.
	IDGenerator idgen.Generator

	// HostCollector gathers host identity and capacity facts.
	// Zero value uses the live machine collector.
	HostCollector HostCollector

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

	// SyslimitsOpts are forwarded to the system limits collector.
	// Zero value uses production defaults.
	SyslimitsOpts syslimits.Options

	// NetCmdRunner is forwarded to the network state collector.
	// Zero value uses the real commands.
	NetCmdRunner netstate.CmdRunner

	// StorageOpts are forwarded to the storage state collector.
	// Zero value uses live filesystem paths.
	StorageOpts storagestate.CollectOptions

	// ServicesOpts are forwarded to the launchd services collector.
	// Zero value collects system LaunchDaemons.
	ServicesOpts services.Options

	// UpdatesOpts are forwarded to the install.log collector.
	UpdatesOpts updates.Query

	// JVMOpts are forwarded to the JVM collector.
	// Zero value uses the real jps/jcmd commands.
	JVMOpts jvm.CollectOptions

	// JVMTelemetryOpts are forwarded to the JVM telemetry adapter.
	// Zero value keeps telemetry lightweight and uses JVMOpts for discovery.
	JVMTelemetryOpts jvm.TelemetryOptions

	// RuntimeTelemetryCollectors can add runtime-neutral telemetry from other
	// application architectures. JVM telemetry is collected separately unless
	// SkipJVMs is true.
	RuntimeTelemetryCollectors []telemetry.Collector

	// SkipJVMs disables JVM process discovery (faster for tests; requires jps
	// in PATH for real collection).
	SkipJVMs bool

	// SkipApps disables the per-app Detect() pass entirely. Useful for
	// host-only snapshots where app data is not needed (e.g. daemon
	// periodic captures).
	SkipApps bool

	// SkipServices disables launchd services collection.
	SkipServices bool

	// SkipUpdates disables macOS install/update log collection.
	SkipUpdates bool

	// DetectStore is the sharded cache for detect.Result JSON. When non-nil,
	// collectApps serves cached results keyed by Info.plist + main-exe hash and
	// stores new results on miss.
	DetectStore *cache.ShardedStore

	// DetectWriter optionally writes detect-cache misses asynchronously.
	// DetectStore must also be set; nil writes synchronously.
	DetectWriter *cache.AsyncWriter

	// ToolchainCache is a TTL cache for the toolchain.Toolchains JSON.
	// Toolchains rarely change within a session; a 5-minute TTL is enough
	// to amortize repeated rules invocations.
	ToolchainCache *cache.TTLStore

	// StorageCache is a TTL cache for storagestate.State JSON. Storage walks
	// can be slow (~Library on a developer machine is large); a 30-second
	// TTL keeps results meaningfully fresh while collapsing close-together
	// invocations.
	StorageCache *cache.TTLStore
}

// Build assembles a Snapshot by running every collector in parallel and
// composing their results. Any collector failure is silently absorbed
// per the system-inventory contract — partial snapshots are valid.
func Build(ctx context.Context, opts Options) Snapshot {
	clk := opts.Clock
	if clk == nil {
		clk = clock.System{}
	}
	takenAt := clk.Now().UTC()
	s := Snapshot{
		ID:      newIDWith(takenAt, opts.IDGenerator),
		TakenAt: takenAt,
		Kind:    KindLive,
	}
	appPaths := snapshotAppPaths(opts)

	siRun := opts.SysinfoCmdRunner
	if siRun == nil {
		siRun = sysinfo.DefaultRunner
	}
	netRun := opts.NetCmdRunner
	if netRun == nil {
		netRun = netstate.DefaultRunner
	}

	// Collectors run concurrently; warnings about degraded collection are
	// appended under this mutex and attached to the snapshot after Wait.
	var warnMu sync.Mutex
	var warnings []string
	addWarning := func(w string) {
		warnMu.Lock()
		warnings = append(warnings, w)
		warnMu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(snapshotCollectorCount(opts))

	go func() {
		defer wg.Done()
		s.Host = hostCollectorFor(opts).CollectHost(opts.SpectraVersion)
		s.HostFacts = s.Host.Facts
	}()
	if !opts.SkipApps {
		go func() {
			defer wg.Done()
			appOpts := opts
			appOpts.AppPaths = appPaths
			s.Apps = collectApps(ctx, appOpts)
		}()
	}
	go func() {
		defer wg.Done()
		s.Toolchains = collectToolchainCached(ctx, opts.ToolchainOpts, opts.ToolchainCache)
	}()
	go func() {
		defer wg.Done()
		s.Power = sysinfo.CollectPower(siRun)
	}()
	go func() {
		defer wg.Done()
		s.SystemLimits = syslimits.Collect(opts.SyslimitsOpts)
	}()
	go func() {
		defer wg.Done()
		s.Sysctls = sysinfo.CollectSysctls(siRun)
	}()
	go func() {
		defer wg.Done()
		s.FDLimit = sysinfo.CollectFDLimit(siRun)
	}()
	go func() {
		defer wg.Done()
		s.Network = netstate.Collect(netRun)
	}()
	if !opts.SkipStorage {
		go func() {
			defer wg.Done()
			s.Storage = collectStorageCached(opts.StorageOpts, opts.StorageCache)
		}()
	}
	if !opts.SkipServices {
		go func() {
			defer wg.Done()
			if inv, err := services.List(ctx, opts.ServicesOpts); err == nil {
				s.Services = inv
			}
		}()
	}
	if !opts.SkipUpdates {
		go func() {
			defer wg.Done()
			s.Updates = collectUpdates(ctx, opts.UpdatesOpts)
		}()
	}

	if !opts.SkipProcesses {
		go func() {
			defer wg.Done()
			processOpts := opts.ProcessOpts
			if len(processOpts.BundlePaths) == 0 {
				processOpts.BundlePaths = appPaths
			}
			s.Processes = process.CollectAll(ctx, processOpts)
		}()
	}
	if !opts.SkipJVMs {
		go func() {
			defer wg.Done()
			infos, jpsAvailable := jvm.CollectAllStatus(ctx, opts.JVMOpts)
			s.JVMs = infos
			if !jpsAvailable {
				addWarning("jvm: jps not found in PATH — JVM discovery unavailable; JVM findings may be incomplete")
			}
		}()
	}
	if len(opts.RuntimeTelemetryCollectors) > 0 {
		go func() {
			defer wg.Done()
			s.RuntimeTelemetry = collectRuntimeTelemetry(ctx, opts.RuntimeTelemetryCollectors)
		}()
	}

	wg.Wait()
	s.Warnings = warnings
	jvm.AttributeJDKs(s.JVMs, s.Toolchains.JDKs)
	finalizeJVMData(ctx, &s, opts)
	attributeRuntimeJDKs(s.RuntimeTelemetry, s.Toolchains.JDKs)
	return s
}

// finalizeJVMData runs the post-Wait JVM steps that need collectors already
// joined: runtime telemetry, and OOM log scanning (which needs both JVMs and
// process LogFiles). Kept out of Build to hold Build's cyclomatic complexity
// under the gate.
func finalizeJVMData(ctx context.Context, s *Snapshot, opts Options) {
	if opts.SkipJVMs {
		return
	}
	s.RuntimeTelemetry = append(s.RuntimeTelemetry, collectJVMTelemetry(ctx, s.JVMs, opts)...)
	if !opts.SkipProcesses {
		s.OOMReports = collectOOMReports(s.JVMs, s.Processes)
	}
}

func snapshotCollectorCount(opts Options) int {
	collectors := 7 // host, toolchains, power, system limits, sysctls, fd limit, network
	if !opts.SkipApps {
		collectors++
	}
	if !opts.SkipProcesses {
		collectors++
	}
	if !opts.SkipStorage {
		collectors++
	}
	if !opts.SkipServices {
		collectors++
	}
	if !opts.SkipUpdates {
		collectors++
	}
	if !opts.SkipJVMs {
		collectors++
	}
	if len(opts.RuntimeTelemetryCollectors) > 0 {
		collectors++
	}
	return collectors
}

func collectUpdates(ctx context.Context, query updates.Query) updates.Result {
	var result updates.Result
	if logs, err := updates.QueryInstallLog(query); err == nil {
		result = logs
	}
	if history, err := updates.Collect(ctx); err == nil {
		result.History = history
	}
	return result
}

func hostCollectorFor(opts Options) HostCollector {
	if opts.HostCollector != nil {
		return opts.HostCollector
	}
	return LiveHostCollector{}
}

func snapshotAppPaths(opts Options) []string {
	if len(opts.AppPaths) > 0 {
		paths := append([]string(nil), opts.AppPaths...)
		sort.Strings(paths)
		return paths
	}
	if opts.SkipApps {
		return nil
	}
	paths := append(scanApps("/Applications"), scanApps("/Applications/Utilities")...)
	sort.Strings(paths)
	return paths
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
				r, err := detectWithCache(paths[i], opts.DetectOpts, opts.DetectStore, opts.DetectWriter)
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
	return newIDWith(time.Now().UTC(), nil)
}

func newIDWith(now time.Time, ids idgen.Generator) string {
	if ids != nil {
		return ids.Next()
	}
	short := now.Format("150405.000000")
	short = strings.ReplaceAll(short, ".", "")
	return "snap-" + now.Format("20060102T150405Z") + "-" + short[len(short)-4:]
}
