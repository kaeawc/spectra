package jvm

import (
	"context"
	"time"

	"github.com/kaeawc/spectra/internal/telemetry"
)

// TelemetryOptions configure JVM runtime telemetry collection.
type TelemetryOptions struct {
	CollectOptions
	// IncludeDiagnostics embeds VM-internal diagnostic sections such as
	// GC.heap_info and VM.native_memory. These may be verbose, so callers can
	// keep snapshots lightweight by leaving this false.
	IncludeDiagnostics bool
	// AgentClient fetches in-process probes when the spectra agent is attached.
	// Zero value discovers agent status through jcmd.
	AgentClient AgentClient
	// Clock stamps each process telemetry record. Nil means time.Now.
	Clock func() time.Time
}

// CollectTelemetry discovers running JVMs and returns runtime-neutral telemetry.
func CollectTelemetry(ctx context.Context, opts TelemetryOptions) []telemetry.Process {
	infos := CollectAll(ctx, opts.CollectOptions)
	if len(infos) == 0 {
		return nil
	}
	out := make([]telemetry.Process, 0, len(infos))
	for _, info := range infos {
		out = append(out, TelemetryForInfo(info, opts))
	}
	return out
}

// TelemetryForInfo maps JVM-specific inspection results into the generic
// runtime telemetry shape used by snapshots.
func TelemetryForInfo(info Info, opts TelemetryOptions) telemetry.Process {
	collected := time.Now().UTC()
	if opts.Clock != nil {
		collected = opts.Clock().UTC()
	}
	proc := telemetry.Process{
		Runtime:   telemetry.RuntimeJVM,
		PID:       info.PID,
		Identity:  jvmTelemetryIdentity(info),
		Config:    jvmTelemetryConfig(info),
		Collected: collected,
	}
	if info.ThreadCount > 0 {
		proc.Threads = &telemetry.Threads{Live: info.ThreadCount, Source: "jcmd Thread.print"}
	}
	if info.GC != nil {
		proc.GC = telemetryFromGCStats(*info.GC)
		proc.Heap = heapFromGCStats(*info.GC)
	}

	client := opts.AgentClient
	if client.StatusProvider != nil || client.Transport != nil {
		applyAgentProbes(&proc, info.PID, client)
	}
	if opts.IncludeDiagnostics {
		applyVMMemoryDiagnostics(&proc, CollectVMMemoryDiagnostics(info.PID, opts.CmdRunner))
	}
	return proc
}

func jvmTelemetryIdentity(info Info) map[string]string {
	fields := map[string]string{
		"main_class":     info.MainClass,
		"java_home":      info.JavaHome,
		"jdk_vendor":     info.JDKVendor,
		"jdk_version":    info.JDKVersion,
		"jdk_install_id": info.JDKInstallID,
		"jdk_source":     info.JDKSource,
		"jdk_path":       info.JDKPath,
	}
	return compactStringMap(fields)
}

func jvmTelemetryConfig(info Info) map[string]string {
	fields := map[string]string{
		"vm_args":  info.VMArgs,
		"vm_flags": info.VMFlags,
	}
	for k, v := range info.SysProps {
		if v != "" {
			fields["sysprop."+k] = v
		}
	}
	return compactStringMap(fields)
}

func compactStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v != "" {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func telemetryFromGCStats(gc GCStats) *telemetry.GC {
	totalCollections := gc.YGC + gc.FGC
	totalMS := int64((gc.YGCT + gc.FGCT) * 1000)
	return &telemetry.GC{
		Collections:      totalCollections,
		CollectionTimeMS: totalMS,
		Source:           "jstat -gc",
		Pools: []telemetry.GCPool{
			{Name: "survivor0", CapacityBytes: kibToBytes(gc.S0C), UsedBytes: kibToBytes(gc.S0U)},
			{Name: "survivor1", CapacityBytes: kibToBytes(gc.S1C), UsedBytes: kibToBytes(gc.S1U)},
			{Name: "eden", CapacityBytes: kibToBytes(gc.EC), UsedBytes: kibToBytes(gc.EU), Collections: gc.YGC, CollectionTime: int64(gc.YGCT * 1000)},
			{Name: "old", CapacityBytes: kibToBytes(gc.OC), UsedBytes: kibToBytes(gc.OU), Collections: gc.FGC, CollectionTime: int64(gc.FGCT * 1000)},
			{Name: "metaspace", CapacityBytes: kibToBytes(gc.MC), UsedBytes: kibToBytes(gc.MU)},
			{Name: "compressed_class_space", CapacityBytes: kibToBytes(gc.CCSC), UsedBytes: kibToBytes(gc.CCSU)},
		},
	}
}

func heapFromGCStats(gc GCStats) *telemetry.Heap {
	return &telemetry.Heap{
		UsedBytes:      kibToBytes(gc.S0U + gc.S1U + gc.EU + gc.OU + gc.MU + gc.CCSU),
		CommittedBytes: kibToBytes(gc.S0C + gc.S1C + gc.EC + gc.OC + gc.MC + gc.CCSC),
		Source:         "jstat -gc",
	}
}

func applyAgentProbes(proc *telemetry.Process, pid int, client AgentClient) {
	probes, err := client.Probes(pid)
	if err != nil {
		proc.Sections = append(proc.Sections, telemetry.Section{Name: "jvm.agent.probes", Error: err.Error()})
		return
	}
	if probes.Runtime.TotalMemory > 0 || probes.Runtime.FreeMemory > 0 || probes.Runtime.MaxMemory > 0 {
		proc.Heap = &telemetry.Heap{
			UsedBytes:      probes.Runtime.TotalMemory - probes.Runtime.FreeMemory,
			CommittedBytes: probes.Runtime.TotalMemory,
			MaxBytes:       probes.Runtime.MaxMemory,
			Source:         "spectra-agent probes",
		}
	}
	if probes.Threads.Live > 0 {
		proc.Threads = &telemetry.Threads{Live: probes.Threads.Live, Source: "spectra-agent probes"}
	}
}

func applyVMMemoryDiagnostics(proc *telemetry.Process, diagnostics VMMemoryDiagnostics) {
	add := func(name string, section DiagnosticSection) {
		proc.Sections = append(proc.Sections, telemetry.Section{
			Name:    name,
			Command: section.Command,
			Output:  section.Output,
			Error:   section.Error,
		})
	}
	add("jvm.heap_info", diagnostics.HeapInfo)
	add("jvm.metaspace", diagnostics.Metaspace)
	add("jvm.native_memory", diagnostics.NativeMemory)
	add("jvm.classloader_stats", diagnostics.ClassLoaderStats)
	add("jvm.code_cache", diagnostics.CodeCache)
	if len(diagnostics.CodeHeap.Command) > 0 || diagnostics.CodeHeap.Output != "" || diagnostics.CodeHeap.Error != "" {
		add("jvm.code_heap", diagnostics.CodeHeap)
	}
}

func kibToBytes(kib float64) int64 {
	if kib <= 0 {
		return 0
	}
	return int64(kib * 1024)
}
