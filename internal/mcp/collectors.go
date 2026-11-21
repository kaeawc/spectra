package mcp

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/corefile"
	"github.com/kaeawc/spectra/internal/dbinspect"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/playbook"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/syslimits"
	"github.com/kaeawc/spectra/internal/timemachine"
	"github.com/kaeawc/spectra/internal/toolchain"
	"github.com/kaeawc/spectra/internal/updates"
)

// Collectors groups host-inspection dependencies behind interfaces so MCP
// handlers can be tested without shelling out to macOS inspection tools.
type Collectors struct {
	Apps        AppInspector
	Processes   ProcessCollector
	Network     NetworkCollector
	Snapshots   SnapshotCollector
	JVMs        JVMCollector
	Toolchain   ToolchainCollector
	DB          DBInspector
	Power       PowerCollector
	Memory      MemoryCollector
	Storage     StorageCollector
	System      SystemCollector
	Services    ServicesCollector
	Logs        LogCollector
	Updates     UpdatesCollector
	TimeMachine TimeMachineCollector
	CoreFiles   CoreFileInspector
	Playbooks   PlaybookCatalog
	// Reports supplies the host seams of the CLI-shaped report tools (load,
	// orphans, idle, owner, spawns); nil fields use the real host.
	Reports ReportDeps
	Clock   Clock
}

type AppInspector interface {
	InspectApp(path string, opts detect.Options) (detect.Result, error)
}

type ProcessCollector interface {
	CollectProcesses(ctx context.Context, opts process.CollectOptions) []process.Info
	SampleProcess(ctx context.Context, pid, durationSec, intervalMS int) (string, error)
}

type NetworkCollector interface {
	CollectNetworkState(ctx context.Context) netstate.State
	CollectConnections(ctx context.Context) []netstate.Connection
}

type SnapshotCollector interface {
	BuildSnapshot(ctx context.Context, opts snapshot.Options) snapshot.Snapshot
}

type JVMCollector interface {
	CollectJVMs(ctx context.Context, opts jvm.CollectOptions) []jvm.Info
	InspectJVM(ctx context.Context, pid int, opts jvm.CollectOptions) *jvm.Info
	CollectExplanation(ctx context.Context, pid int, opts jvm.ExplainOptions) (*jvm.Explanation, error)
	CollectGCStats(ctx context.Context, pid int) (*jvm.GCStats, error)
	CollectVMMemoryDiagnostics(ctx context.Context, pid int) jvm.VMMemoryDiagnostics
	ThreadDump(ctx context.Context, pid int) ([]byte, error)
	HeapHistogram(ctx context.Context, pid int) ([]byte, error)
	HeapDump(ctx context.Context, pid int, dest string) error
	CaptureFlamegraph(ctx context.Context, pid int, opts jvm.FlamegraphOptions) error
}

type ToolchainCollector interface {
	CollectToolchains(ctx context.Context, opts toolchain.CollectOptions) toolchain.Toolchains
}

// DBInspector opens read-only sessions against databases an application
// under debug talks to. Live-socket discovery goes through NetworkCollector;
// this interface covers env and open-file discovery plus the catalog reads.
type DBInspector interface {
	DiscoverDBEnv() []dbinspect.EnvHint
	DiscoverSQLiteFiles(ctx context.Context) []dbinspect.FileCandidate
	Overview(ctx context.Context, dsn string) (*dbinspect.Overview, error)
	Schema(ctx context.Context, dsn, schema string) (*dbinspect.SchemaReport, error)
	Relations(ctx context.Context, dsn, schema string) (*dbinspect.RelationsReport, error)
	Stats(ctx context.Context, dsn, schema string) (*dbinspect.StatsReport, error)
	Sample(ctx context.Context, dsn, table string, limit int) (*dbinspect.SampleReport, error)
}

// PowerCollector reports battery/thermal state and per-pid energy sampling.
type PowerCollector interface {
	CollectPower(ctx context.Context) sysinfo.PowerState
	CollectAssertions(ctx context.Context) []sysinfo.PowerAssertion
	SampleEnergy(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error)
}

// MemoryCollector reports VM compressor, swap, and pressure state.
type MemoryCollector interface {
	Collect() (memstate.MemoryState, error)
}

// StorageCollector reports disk volumes and ~/Library footprint.
type StorageCollector interface {
	Collect(opts storagestate.CollectOptions) storagestate.State
}

// SystemCollector reports kernel resource limits and top holders.
type SystemCollector interface {
	Collect(ctx context.Context, opts syslimits.Options) syslimits.SystemLimits
	CollectTopHolders(ctx context.Context, limit int, opts process.CollectOptions) syslimits.TopHolders
}

// ServicesCollector reports launchd jobs and plist schedules.
type ServicesCollector interface {
	List(ctx context.Context, opts services.Options) (services.LaunchInventory, error)
}

// LogCollector runs bounded unified-log queries.
type LogCollector interface {
	Run(ctx context.Context, q logquery.Query) (logquery.Result, error)
}

// UpdatesCollector reports macOS install/update history and install-log entries.
type UpdatesCollector interface {
	CollectHistory(ctx context.Context) (updates.InstallHistory, error)
	QueryInstallLog(q updates.Query) (updates.Result, error)
}

// TimeMachineCollector reports Time Machine status, destinations, and snapshots.
type TimeMachineCollector interface {
	Collect(ctx context.Context) (timemachine.TimeMachineState, error)
}

// CoreFileInspector inspects crashed-process core files.
type CoreFileInspector interface {
	Inspect(ctx context.Context, path, executablePath string) (corefile.Report, error)
}

// PlaybookCatalog exposes the built-in diagnostic playbooks.
type PlaybookCatalog interface {
	List() []playbook.Playbook
	Get(id string) (playbook.Playbook, bool)
}

type Clock interface {
	Now() time.Time
}

// footprintStore is the on-disk store behind the ~/Library footprint, the
// same one the CLI uses (cache kind "storage"), or nil when the cache root is
// unavailable. A restarted server then answers from the entry a previous
// process or the CLI wrote instead of walking in the foreground.
var footprintStore = sync.OnceValue(func() *cache.TTLStore {
	root, err := cache.DefaultRoot()
	if err != nil {
		return nil
	}
	return cache.NewTTLStore(cache.NewShardedStore(root, cache.KindStorage), nil)
})

// newFootprintCache wraps store (may be nil) in a serve-stale-capable cache.
func newFootprintCache(store *cache.TTLStore) *snapshot.FootprintCache {
	return snapshot.NewFootprintCache(store)
}

func defaultCollectors() Collectors {
	// The MCP server is long-lived, so one footprint cache shared by the
	// storage and snapshot tools can serve a stale ~/Library footprint
	// while a single background walk refreshes it. Snapshots that get
	// persisted read it fresh-only (FootprintPolicy's zero value).
	footprint := newFootprintCache(footprintStore())
	return Collectors{
		Apps:        defaultAppInspector{},
		Processes:   defaultProcessCollector{},
		Network:     defaultNetworkCollector{},
		Snapshots:   defaultSnapshotCollector{footprint: footprint},
		JVMs:        defaultJVMCollector{},
		Toolchain:   defaultToolchainCollector{},
		DB:          defaultDBInspector{},
		Power:       defaultPowerCollector{},
		Memory:      defaultMemoryCollector{},
		Storage:     defaultStorageCollector{footprint: footprint},
		System:      defaultSystemCollector{},
		Services:    defaultServicesCollector{},
		Logs:        defaultLogCollector{},
		Updates:     defaultUpdatesCollector{},
		TimeMachine: defaultTimeMachineCollector{},
		CoreFiles:   defaultCoreFileInspector{},
		Playbooks:   defaultPlaybookCatalog{},
		Reports:     defaultReportDeps(),
		Clock:       systemClock{},
	}
}

type defaultDBInspector struct{}

func (defaultDBInspector) DiscoverDBEnv() []dbinspect.EnvHint {
	return dbinspect.DiscoverEnv(os.Getenv)
}

func (defaultDBInspector) DiscoverSQLiteFiles(ctx context.Context) []dbinspect.FileCandidate {
	return dbinspect.DiscoverSQLiteFiles(proc.BoundedRunner(ctx))
}

func (defaultDBInspector) Overview(ctx context.Context, dsn string) (*dbinspect.Overview, error) {
	return dbinspect.CollectOverview(ctx, dsn, dbinspect.Options{})
}

func (defaultDBInspector) Schema(ctx context.Context, dsn, schema string) (*dbinspect.SchemaReport, error) {
	return dbinspect.CollectSchema(ctx, dsn, schema, dbinspect.Options{})
}

func (defaultDBInspector) Relations(ctx context.Context, dsn, schema string) (*dbinspect.RelationsReport, error) {
	return dbinspect.CollectRelations(ctx, dsn, schema, dbinspect.Options{})
}

func (defaultDBInspector) Stats(ctx context.Context, dsn, schema string) (*dbinspect.StatsReport, error) {
	return dbinspect.CollectStats(ctx, dsn, schema, dbinspect.Options{})
}

func (defaultDBInspector) Sample(ctx context.Context, dsn, table string, limit int) (*dbinspect.SampleReport, error) {
	return dbinspect.SampleTable(ctx, dsn, table, limit, dbinspect.Options{})
}

type defaultAppInspector struct{}

func (defaultAppInspector) InspectApp(path string, opts detect.Options) (detect.Result, error) {
	return detect.DetectWith(path, opts)
}

type defaultProcessCollector struct{}

func (defaultProcessCollector) CollectProcesses(ctx context.Context, opts process.CollectOptions) []process.Info {
	return process.CollectAll(ctx, opts)
}

func (defaultProcessCollector) SampleProcess(ctx context.Context, pid, durationSec, intervalMS int) (string, error) {
	return sampleProcess(ctx, pid, durationSec, intervalMS)
}

type defaultNetworkCollector struct{}

func (defaultNetworkCollector) CollectNetworkState(ctx context.Context) netstate.State {
	return netstate.Collect(netstate.ContextRunner(ctx))
}

func (defaultNetworkCollector) CollectConnections(ctx context.Context) []netstate.Connection {
	return netstate.CollectConnections(netstate.ContextRunner(ctx))
}

type defaultSnapshotCollector struct {
	footprint *snapshot.FootprintCache
}

func (c defaultSnapshotCollector) BuildSnapshot(ctx context.Context, opts snapshot.Options) snapshot.Snapshot {
	if opts.FootprintCache == nil {
		opts.FootprintCache = c.footprint
	}
	return snapshot.Build(ctx, opts)
}

type defaultJVMCollector struct{}

func (defaultJVMCollector) CollectJVMs(ctx context.Context, opts jvm.CollectOptions) []jvm.Info {
	return jvm.CollectAll(ctx, killableJVMOptions(ctx, opts))
}

func (defaultJVMCollector) InspectJVM(ctx context.Context, pid int, opts jvm.CollectOptions) *jvm.Info {
	return jvm.InspectPID(ctx, pid, killableJVMOptions(ctx, opts))
}

// killableJVMOptions fills in runners that kill their child when the tool
// call's ctx ends; the jvm package's plain default ignores ctx.
func killableJVMOptions(ctx context.Context, opts jvm.CollectOptions) jvm.CollectOptions {
	if opts.CmdRunner == nil && opts.CmdRunnerContext == nil {
		opts.CmdRunner = jvm.ContextRunner(ctx)
		opts.CmdRunnerContext = jvm.DefaultCtxRunner
	}
	return opts
}

func (defaultJVMCollector) CollectExplanation(ctx context.Context, pid int, opts jvm.ExplainOptions) (*jvm.Explanation, error) {
	if opts.CmdRunner == nil {
		opts.CmdRunner = jvm.ContextRunner(ctx)
	}
	return jvm.CollectExplanation(ctx, pid, opts)
}

func (defaultJVMCollector) CollectGCStats(ctx context.Context, pid int) (*jvm.GCStats, error) {
	return jvm.CollectGCStats(pid, jvm.ContextRunner(ctx))
}

func (defaultJVMCollector) CollectVMMemoryDiagnostics(ctx context.Context, pid int) jvm.VMMemoryDiagnostics {
	return jvm.CollectVMMemoryDiagnostics(pid, jvm.ContextRunner(ctx))
}

func (defaultJVMCollector) ThreadDump(ctx context.Context, pid int) ([]byte, error) {
	return jvm.ThreadDump(pid, jvm.ContextRunner(ctx))
}

func (defaultJVMCollector) HeapHistogram(ctx context.Context, pid int) ([]byte, error) {
	return jvm.HeapHistogram(pid, jvm.ContextRunner(ctx))
}

func (defaultJVMCollector) HeapDump(ctx context.Context, pid int, dest string) error {
	return jvm.HeapDump(pid, dest, jvm.ContextRunner(ctx))
}

func (defaultJVMCollector) CaptureFlamegraph(ctx context.Context, pid int, opts jvm.FlamegraphOptions) error {
	if opts.CmdRunner == nil {
		opts.CmdRunner = jvm.ContextRunner(ctx)
	}
	return jvm.CaptureFlamegraph(pid, opts)
}

type defaultToolchainCollector struct{}

func (defaultToolchainCollector) CollectToolchains(ctx context.Context, opts toolchain.CollectOptions) toolchain.Toolchains {
	return toolchain.Collect(ctx, opts)
}

type defaultPowerCollector struct{}

func (defaultPowerCollector) CollectPower(ctx context.Context) sysinfo.PowerState {
	return sysinfo.CollectPower(proc.BoundedRunner(ctx))
}

func (defaultPowerCollector) CollectAssertions(ctx context.Context) []sysinfo.PowerAssertion {
	return sysinfo.CollectAssertions(proc.BoundedRunner(ctx))
}

func (defaultPowerCollector) SampleEnergy(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
	return sysinfo.EnergySampler{Interval: interval}.Sample(ctx, pids)
}

type defaultMemoryCollector struct{}

func (defaultMemoryCollector) Collect() (memstate.MemoryState, error) {
	return memstate.Collect()
}

type defaultStorageCollector struct {
	footprint *snapshot.FootprintCache
}

// Collect reads df/mount data live and the footprint through the shared
// serve-stale cache.
func (c defaultStorageCollector) Collect(opts storagestate.CollectOptions) storagestate.State {
	return snapshot.CollectStorage(opts, nil, c.footprint, snapshot.FootprintServeStale)
}

type defaultSystemCollector struct{}

func (defaultSystemCollector) Collect(ctx context.Context, opts syslimits.Options) syslimits.SystemLimits {
	if opts.Run == nil {
		opts.Run = proc.BoundedRunner(ctx)
	}
	return syslimits.Collect(opts)
}

func (defaultSystemCollector) CollectTopHolders(ctx context.Context, limit int, opts process.CollectOptions) syslimits.TopHolders {
	return syslimits.CollectTopHolders(ctx, limit, opts)
}

type defaultServicesCollector struct{}

func (defaultServicesCollector) List(ctx context.Context, opts services.Options) (services.LaunchInventory, error) {
	return services.List(ctx, opts)
}

type defaultLogCollector struct{}

func (defaultLogCollector) Run(ctx context.Context, q logquery.Query) (logquery.Result, error) {
	return logquery.Run(ctx, q)
}

type defaultUpdatesCollector struct{}

func (defaultUpdatesCollector) CollectHistory(ctx context.Context) (updates.InstallHistory, error) {
	return updates.Collect(ctx)
}

func (defaultUpdatesCollector) QueryInstallLog(q updates.Query) (updates.Result, error) {
	return updates.QueryInstallLog(q)
}

type defaultTimeMachineCollector struct{}

func (defaultTimeMachineCollector) Collect(ctx context.Context) (timemachine.TimeMachineState, error) {
	return timemachine.Collect(ctx)
}

type defaultCoreFileInspector struct{}

func (defaultCoreFileInspector) Inspect(ctx context.Context, path, executablePath string) (corefile.Report, error) {
	return corefile.Inspector{}.Inspect(ctx, path, executablePath)
}

type defaultPlaybookCatalog struct{}

func (defaultPlaybookCatalog) List() []playbook.Playbook {
	return playbook.MustDefaultCatalog().List()
}

func (defaultPlaybookCatalog) Get(id string) (playbook.Playbook, bool) {
	return playbook.MustDefaultCatalog().Get(id)
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
