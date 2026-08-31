package mcp

import (
	"context"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/corefile"
	"github.com/kaeawc/spectra/internal/dbinspect"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/playbook"
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
	Clock       Clock
}

type AppInspector interface {
	InspectApp(path string, opts detect.Options) (detect.Result, error)
}

type ProcessCollector interface {
	CollectProcesses(ctx context.Context, opts process.CollectOptions) []process.Info
	SampleProcess(pid, durationSec, intervalMS int) (string, error)
}

type NetworkCollector interface {
	CollectNetworkState() netstate.State
	CollectConnections() []netstate.Connection
}

type SnapshotCollector interface {
	BuildSnapshot(ctx context.Context, opts snapshot.Options) snapshot.Snapshot
}

type JVMCollector interface {
	CollectJVMs(ctx context.Context, opts jvm.CollectOptions) []jvm.Info
	InspectJVM(ctx context.Context, pid int, opts jvm.CollectOptions) *jvm.Info
	CollectExplanation(ctx context.Context, pid int, opts jvm.ExplainOptions) (*jvm.Explanation, error)
	CollectGCStats(pid int) (*jvm.GCStats, error)
	CollectVMMemoryDiagnostics(pid int) jvm.VMMemoryDiagnostics
	ThreadDump(pid int) ([]byte, error)
	HeapHistogram(pid int) ([]byte, error)
	HeapDump(pid int, dest string) error
	CaptureFlamegraph(pid int, opts jvm.FlamegraphOptions) error
}

type ToolchainCollector interface {
	CollectToolchains(ctx context.Context, opts toolchain.CollectOptions) toolchain.Toolchains
}

// DBInspector opens read-only sessions against databases an application
// under debug talks to. Live-socket discovery goes through NetworkCollector;
// this interface covers env discovery and the catalog reads.
type DBInspector interface {
	DiscoverDBEnv() []dbinspect.EnvHint
	Overview(ctx context.Context, dsn string) (*dbinspect.Overview, error)
	Schema(ctx context.Context, dsn, schema string) (*dbinspect.SchemaReport, error)
	Relations(ctx context.Context, dsn, schema string) (*dbinspect.RelationsReport, error)
	Stats(ctx context.Context, dsn, schema string) (*dbinspect.StatsReport, error)
	Sample(ctx context.Context, dsn, table string, limit int) (*dbinspect.SampleReport, error)
}

// PowerCollector reports battery/thermal state and per-pid energy sampling.
type PowerCollector interface {
	CollectPower() sysinfo.PowerState
	CollectAssertions() []sysinfo.PowerAssertion
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
	Collect(opts syslimits.Options) syslimits.SystemLimits
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

func defaultCollectors() Collectors {
	return Collectors{
		Apps:        defaultAppInspector{},
		Processes:   defaultProcessCollector{},
		Network:     defaultNetworkCollector{},
		Snapshots:   defaultSnapshotCollector{},
		JVMs:        defaultJVMCollector{},
		Toolchain:   defaultToolchainCollector{},
		DB:          defaultDBInspector{},
		Power:       defaultPowerCollector{},
		Memory:      defaultMemoryCollector{},
		Storage:     defaultStorageCollector{},
		System:      defaultSystemCollector{},
		Services:    defaultServicesCollector{},
		Logs:        defaultLogCollector{},
		Updates:     defaultUpdatesCollector{},
		TimeMachine: defaultTimeMachineCollector{},
		CoreFiles:   defaultCoreFileInspector{},
		Playbooks:   defaultPlaybookCatalog{},
		Clock:       systemClock{},
	}
}

type defaultDBInspector struct{}

func (defaultDBInspector) DiscoverDBEnv() []dbinspect.EnvHint {
	return dbinspect.DiscoverEnv(os.Getenv)
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

func (defaultProcessCollector) SampleProcess(pid, durationSec, intervalMS int) (string, error) {
	return sampleProcess(pid, durationSec, intervalMS)
}

type defaultNetworkCollector struct{}

func (defaultNetworkCollector) CollectNetworkState() netstate.State {
	return netstate.Collect(netstate.DefaultRunner)
}

func (defaultNetworkCollector) CollectConnections() []netstate.Connection {
	return netstate.CollectConnections(netstate.DefaultRunner)
}

type defaultSnapshotCollector struct{}

func (defaultSnapshotCollector) BuildSnapshot(ctx context.Context, opts snapshot.Options) snapshot.Snapshot {
	return snapshot.Build(ctx, opts)
}

type defaultJVMCollector struct{}

func (defaultJVMCollector) CollectJVMs(ctx context.Context, opts jvm.CollectOptions) []jvm.Info {
	return jvm.CollectAll(ctx, opts)
}

func (defaultJVMCollector) InspectJVM(ctx context.Context, pid int, opts jvm.CollectOptions) *jvm.Info {
	return jvm.InspectPID(ctx, pid, opts)
}

func (defaultJVMCollector) CollectExplanation(ctx context.Context, pid int, opts jvm.ExplainOptions) (*jvm.Explanation, error) {
	return jvm.CollectExplanation(ctx, pid, opts)
}

func (defaultJVMCollector) CollectGCStats(pid int) (*jvm.GCStats, error) {
	return jvm.CollectGCStats(pid, nil)
}

func (defaultJVMCollector) CollectVMMemoryDiagnostics(pid int) jvm.VMMemoryDiagnostics {
	return jvm.CollectVMMemoryDiagnostics(pid, nil)
}

func (defaultJVMCollector) ThreadDump(pid int) ([]byte, error) {
	return jvm.ThreadDump(pid, nil)
}

func (defaultJVMCollector) HeapHistogram(pid int) ([]byte, error) {
	return jvm.HeapHistogram(pid, nil)
}

func (defaultJVMCollector) HeapDump(pid int, dest string) error {
	return jvm.HeapDump(pid, dest, nil)
}

func (defaultJVMCollector) CaptureFlamegraph(pid int, opts jvm.FlamegraphOptions) error {
	return jvm.CaptureFlamegraph(pid, opts)
}

type defaultToolchainCollector struct{}

func (defaultToolchainCollector) CollectToolchains(ctx context.Context, opts toolchain.CollectOptions) toolchain.Toolchains {
	return toolchain.Collect(ctx, opts)
}

type defaultPowerCollector struct{}

func (defaultPowerCollector) CollectPower() sysinfo.PowerState {
	return sysinfo.CollectPower(sysinfo.DefaultRunner)
}

func (defaultPowerCollector) CollectAssertions() []sysinfo.PowerAssertion {
	return sysinfo.CollectAssertions(sysinfo.DefaultRunner)
}

func (defaultPowerCollector) SampleEnergy(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
	return sysinfo.EnergySampler{Interval: interval}.Sample(ctx, pids)
}

type defaultMemoryCollector struct{}

func (defaultMemoryCollector) Collect() (memstate.MemoryState, error) {
	return memstate.Collect()
}

type defaultStorageCollector struct{}

func (defaultStorageCollector) Collect(opts storagestate.CollectOptions) storagestate.State {
	return storagestate.Collect(opts)
}

type defaultSystemCollector struct{}

func (defaultSystemCollector) Collect(opts syslimits.Options) syslimits.SystemLimits {
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
