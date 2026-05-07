package mcp

import (
	"context"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// Collectors groups host-inspection dependencies behind interfaces so MCP
// handlers can be tested without shelling out to macOS inspection tools.
type Collectors struct {
	Apps      AppInspector
	Processes ProcessCollector
	Network   NetworkCollector
	Snapshots SnapshotCollector
	JVMs      JVMCollector
	Toolchain ToolchainCollector
	Clock     Clock
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

type Clock interface {
	Now() time.Time
}

func defaultCollectors() Collectors {
	return Collectors{
		Apps:      defaultAppInspector{},
		Processes: defaultProcessCollector{},
		Network:   defaultNetworkCollector{},
		Snapshots: defaultSnapshotCollector{},
		JVMs:      defaultJVMCollector{},
		Toolchain: defaultToolchainCollector{},
		Clock:     systemClock{},
	}
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

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
