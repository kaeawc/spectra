package jvm

import (
	"context"
	"strconv"
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
	if info.Classes != nil {
		proc.Sections = append(proc.Sections, telemetry.Section{
			Name:   "jvm.class_stats",
			Output: classStatsSummary(*info.Classes),
		})
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

func classStatsSummary(stats ClassStats) string {
	return "loaded=" + strconv.FormatInt(stats.Loaded, 10) +
		" loaded_kib=" + strconv.FormatFloat(stats.LoadedKiB, 'f', -1, 64) +
		" unloaded=" + strconv.FormatInt(stats.Unloaded, 10) +
		" unloaded_kib=" + strconv.FormatFloat(stats.UnloadedKiB, 'f', -1, 64) +
		" class_load_time=" + strconv.FormatFloat(stats.ClassLoadTime, 'f', -1, 64)
}

// TelemetrySample is one chart-friendly observation of a JVM process.
type TelemetrySample struct {
	TakenAt time.Time `json:"taken_at"`
	PID     int       `json:"pid"`

	MainClass string `json:"main_class,omitempty"`

	HeapUsedKiB     float64 `json:"heap_used_kib,omitempty"`
	HeapCapacityKiB float64 `json:"heap_capacity_kib,omitempty"`
	HeapMaxBytes    int64   `json:"heap_max_bytes,omitempty"`

	MetaspaceUsedKiB     float64 `json:"metaspace_used_kib,omitempty"`
	MetaspaceCapacityKiB float64 `json:"metaspace_capacity_kib,omitempty"`

	YoungGCCount int64   `json:"young_gc_count,omitempty"`
	YoungGCTime  float64 `json:"young_gc_time_seconds,omitempty"`
	FullGCCount  int64   `json:"full_gc_count,omitempty"`
	FullGCTime   float64 `json:"full_gc_time_seconds,omitempty"`
	TotalGCTime  float64 `json:"total_gc_time_seconds,omitempty"`

	ThreadCount        int   `json:"thread_count,omitempty"`
	AgentThreadCount   int   `json:"agent_thread_count,omitempty"`
	LoadedClassCount   int64 `json:"loaded_class_count,omitempty"`
	VirtualThreadCount int64 `json:"virtual_thread_count,omitempty"`

	AgentAttached bool   `json:"agent_attached"`
	Error         string `json:"error,omitempty"`
}

func (s TelemetrySample) TelemetrySubject() telemetry.Subject {
	return telemetry.Subject{Kind: "process", Runtime: string(telemetry.RuntimeJVM), PID: s.PID, Name: s.MainClass}
}

func (s TelemetrySample) TelemetryTakenAt() time.Time {
	return s.TakenAt
}

// BuildTelemetrySample converts the existing one-shot JVM inspection shape into
// a time-series point that UI clients can plot without parsing jcmd text.
func BuildTelemetrySample(at time.Time, info Info, probes *AgentProbes, err error) TelemetrySample {
	s := TelemetrySample{
		TakenAt:     at.UTC(),
		PID:         info.PID,
		MainClass:   info.MainClass,
		ThreadCount: info.ThreadCount,
	}
	if info.GC != nil {
		s.HeapUsedKiB = info.GC.EU + info.GC.OU
		s.HeapCapacityKiB = info.GC.EC + info.GC.OC
		s.MetaspaceUsedKiB = info.GC.MU
		s.MetaspaceCapacityKiB = info.GC.MC
		s.YoungGCCount = info.GC.YGC
		s.YoungGCTime = info.GC.YGCT
		s.FullGCCount = info.GC.FGC
		s.FullGCTime = info.GC.FGCT
		s.TotalGCTime = info.GC.GCT
	}
	if probes != nil {
		s.AgentAttached = true
		s.HeapMaxBytes = probes.Runtime.MaxMemory
		s.AgentThreadCount = probes.Threads.Live
		s.LoadedClassCount = intCounter(probes.Counters, "loaded_classes")
		s.VirtualThreadCount = intCounter(probes.Counters, "virtual_threads")
	}
	if info.Classes != nil {
		s.LoadedClassCount = info.Classes.Loaded
	}
	if err != nil {
		s.Error = err.Error()
	}
	return s
}

func intCounter(counters []AgentCounter, name string) int64 {
	for _, counter := range counters {
		if counter.Name != name || counter.Error != "" {
			continue
		}
		switch v := counter.Value.(type) {
		case float64:
			if v <= 0 {
				return 0
			}
			if v > float64(^uint64(0)>>1) {
				return int64(^uint64(0) >> 1)
			}
			return int64(v)
		case int:
			return int64(v)
		case int64:
			if v <= 0 {
				return 0
			}
			return v
		}
	}
	return 0
}

// TelemetrySampler periodically samples all visible JVMs and records points in
// a live telemetry collector.
type TelemetrySampler struct {
	collector *telemetry.LiveCollector
	interval  time.Duration
	opts      CollectOptions
	probes    func(pid int) (AgentProbes, error)
}

func NewTelemetrySampler(c *telemetry.LiveCollector, interval time.Duration, opts CollectOptions, probes func(pid int) (AgentProbes, error)) *TelemetrySampler {
	if interval <= 0 {
		interval = time.Second
	}
	if probes == nil {
		probes = func(pid int) (AgentProbes, error) {
			return FetchAgentProbes(pid, opts.CmdRunner)
		}
	}
	return &TelemetrySampler{collector: c, interval: interval, opts: opts, probes: probes}
}

func (s *TelemetrySampler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			s.sample(ctx, t)
		}
	}
}

func (s *TelemetrySampler) sample(ctx context.Context, at time.Time) {
	for _, info := range CollectAll(ctx, s.opts) {
		var probes *AgentProbes
		if s.probes != nil {
			p, err := s.probes(info.PID)
			if err == nil {
				probes = &p
			}
		}
		s.collector.Add(BuildTelemetrySample(at, info, probes, nil))
	}
	s.collector.Flush(telemetry.DefaultLiveRetainWindow, at)
}
