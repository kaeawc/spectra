package jvm

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/toolchain"
)

// ExplainOptions configures JVM explanation collection.
type ExplainOptions struct {
	CmdRunner CmdRunner
	Samples   int
	Interval  time.Duration
	SoftRefs  bool
	JDKs      []toolchain.JDKInstall
}

// Explanation is a structured, operator-facing interpretation of JVM state.
type Explanation struct {
	PID          int               `json:"pid"`
	MainClass    string            `json:"main_class,omitempty"`
	JDKVendor    string            `json:"jdk_vendor,omitempty"`
	JDKVersion   string            `json:"jdk_version,omitempty"`
	JavaHome     string            `json:"java_home,omitempty"`
	VMArgs       string            `json:"vm_args,omitempty"`
	Observations []Observation     `json:"observations"`
	Trend        *ResourceTrend    `json:"trend,omitempty"`
	SoftRefs     *SoftReferenceUse `json:"soft_refs,omitempty"`
}

// Observation is one interpreted JVM signal.
type Observation struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	Summary        string `json:"summary"`
	Evidence       string `json:"evidence,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
}

// ResourceTrend compares JVM counters over repeated samples.
type ResourceTrend struct {
	Samples           int     `json:"samples"`
	IntervalMillis    int64   `json:"interval_millis"`
	OldUsedStartKiB   float64 `json:"old_used_start_kib,omitempty"`
	OldUsedEndKiB     float64 `json:"old_used_end_kib,omitempty"`
	MetaspaceStartKiB float64 `json:"metaspace_start_kib,omitempty"`
	MetaspaceEndKiB   float64 `json:"metaspace_end_kib,omitempty"`
	FullGCStart       int64   `json:"full_gc_start,omitempty"`
	FullGCEnd         int64   `json:"full_gc_end,omitempty"`
	TotalGCTimeStart  float64 `json:"total_gc_time_start,omitempty"`
	TotalGCTimeEnd    float64 `json:"total_gc_time_end,omitempty"`
	OldUsedDeltaKiB   float64 `json:"old_used_delta_kib,omitempty"`
	MetaspaceDeltaKiB float64 `json:"metaspace_delta_kib,omitempty"`
	FullGCDelta       int64   `json:"full_gc_delta,omitempty"`
	TotalGCTimeDelta  float64 `json:"total_gc_time_delta,omitempty"`
}

// SoftReferenceUse summarizes live SoftReference instances from a class
// histogram. It is a point-in-time signal, not proof of retention across GC.
type SoftReferenceUse struct {
	Instances int64 `json:"instances"`
	Bytes     int64 `json:"bytes"`
}

// CollectExplanation gathers and interprets JVM diagnostics for one PID.
func CollectExplanation(ctx context.Context, pid int, opts ExplainOptions) (*Explanation, error) {
	run := opts.CmdRunner
	if run == nil {
		run = DefaultRunner
	}
	info := InspectPID(ctx, pid, CollectOptions{CmdRunner: run, JDKs: opts.JDKs})
	if info == nil {
		return nil, fmt.Errorf("JVM PID %d not found or not inspectable", pid)
	}
	e := &Explanation{
		PID:        info.PID,
		MainClass:  info.MainClass,
		JDKVendor:  info.JDKVendor,
		JDKVersion: info.JDKVersion,
		JavaHome:   info.JavaHome,
		VMArgs:     info.VMArgs,
	}
	if info.GC != nil {
		explainGC(e, info.GC)
	}
	explainVMArgs(e, info.VMArgs)
	explainMemorySections(e, collectExplainMemory(pid, run))
	if opts.SoftRefs {
		explainSoftRefs(e, pid, run)
	}
	if opts.Samples > 1 {
		trend := collectTrend(pid, opts.Samples, opts.Interval, run)
		e.Trend = trend
		explainTrend(e, trend)
	} else {
		e.Observations = append(e.Observations, Observation{
			ID:             "trend-not-collected",
			Severity:       "info",
			Summary:        "Resource trends were not collected in this run.",
			Recommendation: "Run `spectra jvm explain --samples 6 --interval 10s <pid>` or use the daemon metrics history to distinguish a leak from a high steady-state footprint.",
		})
	}
	return e, nil
}

type explainMemory struct {
	HeapInfo         DiagnosticSection
	Metaspace        DiagnosticSection
	NativeMemory     DiagnosticSection
	ClassLoaderStats DiagnosticSection
	CodeCache        DiagnosticSection
}

func collectExplainMemory(pid int, run CmdRunner) explainMemory {
	return explainMemory{
		HeapInfo:         runDiagnostic(pid, run, "GC.heap_info"),
		Metaspace:        runDiagnostic(pid, run, "VM.metaspace"),
		NativeMemory:     runDiagnostic(pid, run, "VM.native_memory", "summary"),
		ClassLoaderStats: runDiagnostic(pid, run, "VM.classloader_stats"),
		CodeCache:        runDiagnostic(pid, run, "Compiler.codecache"),
	}
}

func explainGC(e *Explanation, gc *GCStats) {
	if gc.OC > 0 {
		pct := gc.OU * 100 / gc.OC
		severity := "info"
		recommendation := ""
		if pct >= 90 {
			severity = "warning"
			recommendation = "Capture a heap histogram/JFR allocation profile and compare old-gen after full GC; a single high sample is pressure, not proof of a leak."
		}
		e.Observations = append(e.Observations, Observation{
			ID:             "old-gen-occupancy",
			Severity:       severity,
			Summary:        fmt.Sprintf("Old generation is %.0f%% used.", pct),
			Evidence:       fmt.Sprintf("%.0f KiB used / %.0f KiB capacity", gc.OU, gc.OC),
			Recommendation: recommendation,
		})
	}
	if gc.FGC > 0 {
		severity := "info"
		if gc.FGC >= 5 && gc.FGCT >= 1 {
			severity = "warning"
		}
		e.Observations = append(e.Observations, Observation{
			ID:       "full-gc-count",
			Severity: severity,
			Summary:  fmt.Sprintf("Full GC has run %d times.", gc.FGC),
			Evidence: fmt.Sprintf("%.3fs full-GC time, %.3fs total GC time", gc.FGCT, gc.GCT),
		})
	}
}

func explainVMArgs(e *Explanation, args string) {
	if args == "" {
		e.Observations = append(e.Observations, Observation{ID: "vm-args-missing", Severity: "info", Summary: "VM arguments were not available."})
		return
	}
	if strings.Contains(args, "-XX:+UseSerialGC") {
		e.Observations = append(e.Observations, Observation{
			ID:             "serial-gc",
			Severity:       "info",
			Summary:        "The JVM is using SerialGC.",
			Evidence:       "-XX:+UseSerialGC",
			Recommendation: "This can be intentional for small desktop helper processes; for server or IDE-scale workloads, compare with G1/ZGC under realistic load.",
		})
	}
	if !strings.Contains(args, "-XX:NativeMemoryTracking=") {
		e.Observations = append(e.Observations, Observation{
			ID:             "nmt-disabled-by-args",
			Severity:       "info",
			Summary:        "Native memory tracking was not enabled at startup.",
			Recommendation: "Start with `-XX:NativeMemoryTracking=summary` or `detail` when investigating native memory growth or fragmentation.",
		})
	}
	if strings.Contains(args, "-XX:+HeapDumpOnOutOfMemoryError") {
		e.Observations = append(e.Observations, Observation{ID: "oom-heap-dump-enabled", Severity: "good", Summary: "Heap dump on OOM is enabled."})
	}
	if xmx := parseMemoryArgMiB(args, "-Xmx"); xmx > 0 {
		e.Observations = append(e.Observations, Observation{ID: "xmx", Severity: "info", Summary: fmt.Sprintf("Maximum heap is %.0f MiB.", xmx), Evidence: "-Xmx"})
	}
	if softRefPolicy := parseXXNumberArg(args, "SoftRefLRUPolicyMSPerMB"); softRefPolicy >= 0 {
		e.Observations = append(e.Observations, Observation{
			ID:       "soft-ref-policy",
			Severity: "info",
			Summary:  fmt.Sprintf("Soft reference LRU policy is explicitly set to %.0f ms/MB.", softRefPolicy),
			Evidence: "-XX:SoftRefLRUPolicyMSPerMB",
		})
	}
}

func explainMemorySections(e *Explanation, mem explainMemory) {
	if meta := parseMetaspaceUsage(mem.Metaspace.Output); meta.usedMiB > 0 {
		severity := "info"
		if meta.usedPct >= 90 {
			severity = "warning"
		}
		e.Observations = append(e.Observations, Observation{
			ID:             "metaspace-usage",
			Severity:       severity,
			Summary:        fmt.Sprintf("Metaspace is %.0f%% used within committed capacity.", meta.usedPct),
			Evidence:       fmt.Sprintf("%.2f MiB used, %.2f MiB committed, %d classloaders, %d classes", meta.usedMiB, meta.committedMiB, meta.loaders, meta.classes),
			Recommendation: "A metaspace leak requires growth over time, especially increasing classloader count after workload quiescence.",
		})
	}
	if cls := parseClassLoaderTotal(mem.ClassLoaderStats.Output); cls.classes > 0 {
		e.Observations = append(e.Observations, Observation{
			ID:       "classloader-footprint",
			Severity: "info",
			Summary:  fmt.Sprintf("%d classloaders account for %d loaded classes.", cls.loaders, cls.classes),
			Evidence: fmt.Sprintf("%.2f MiB metaspace chunks, %.2f MiB blocks", float64(cls.chunkBytes)/(1024*1024), float64(cls.blockBytes)/(1024*1024)),
		})
	}
	if code := parseCodeCache(mem.CodeCache.Output); code.sizeKiB > 0 {
		severity := "info"
		if code.usedPct >= 80 || code.fullCount > 0 {
			severity = "warning"
		}
		e.Observations = append(e.Observations, Observation{
			ID:             "code-cache-use",
			Severity:       severity,
			Summary:        fmt.Sprintf("Code cache is %.0f%% used.", code.usedPct),
			Evidence:       fmt.Sprintf("%.0f KiB used / %.0f KiB reserved; nmethods=%d, adapters=%d, full_count=%d", code.usedKiB, code.sizeKiB, code.nmethods, code.adapters, code.fullCount),
			Recommendation: "High nmethod use means JIT-compiled Java methods dominate code cache; adapters/non-nmethods are JVM glue. Investigate only if free space is low or full_count increases.",
		})
	}
	if strings.Contains(strings.ToLower(mem.NativeMemory.Output), "not enabled") {
		e.Observations = append(e.Observations, Observation{
			ID:             "native-memory-unknown",
			Severity:       "unknown",
			Summary:        "Native memory fragmentation cannot be evaluated because NMT is disabled.",
			Evidence:       "VM.native_memory summary reported that native memory tracking is not enabled.",
			Recommendation: "Reproduce with `-XX:NativeMemoryTracking=summary` for category growth, or `detail` when fragmentation/location matters.",
		})
	} else if mem.NativeMemory.Output != "" {
		e.Observations = append(e.Observations, Observation{
			ID:       "native-memory-available",
			Severity: "info",
			Summary:  "Native memory tracking output is available for category analysis.",
		})
	}
}

func explainSoftRefs(e *Explanation, pid int, run CmdRunner) {
	out, err := HeapHistogram(pid, run)
	if err != nil {
		e.Observations = append(e.Observations, Observation{ID: "soft-refs-unknown", Severity: "unknown", Summary: "Soft reference use could not be checked.", Evidence: err.Error()})
		return
	}
	refs := parseSoftReferences(string(out))
	e.SoftRefs = &refs
	if refs.Instances == 0 {
		e.Observations = append(e.Observations, Observation{ID: "soft-refs-none", Severity: "good", Summary: "No live java.lang.ref.SoftReference instances were visible in the class histogram."})
		return
	}
	e.Observations = append(e.Observations, Observation{
		ID:             "soft-refs-present",
		Severity:       "info",
		Summary:        fmt.Sprintf("%d live SoftReference instances are present.", refs.Instances),
		Evidence:       fmt.Sprintf("%d bytes attributed to java.lang.ref.SoftReference objects", refs.Bytes),
		Recommendation: "To prove whether they are kept, compare histograms before/after explicit GC or capture JFR reference statistics under memory pressure.",
	})
}

func collectTrend(pid, samples int, interval time.Duration, run CmdRunner) *ResourceTrend {
	if samples < 2 {
		return nil
	}
	if interval <= 0 {
		interval = time.Second
	}
	var first, last *GCStats
	for i := 0; i < samples; i++ {
		gc, err := CollectGCStats(pid, run)
		if err == nil {
			if first == nil {
				first = gc
			}
			last = gc
		}
		if i != samples-1 {
			time.Sleep(interval)
		}
	}
	if first == nil || last == nil {
		return &ResourceTrend{Samples: samples, IntervalMillis: interval.Milliseconds()}
	}
	return &ResourceTrend{
		Samples:           samples,
		IntervalMillis:    interval.Milliseconds(),
		OldUsedStartKiB:   first.OU,
		OldUsedEndKiB:     last.OU,
		MetaspaceStartKiB: first.MU,
		MetaspaceEndKiB:   last.MU,
		FullGCStart:       first.FGC,
		FullGCEnd:         last.FGC,
		TotalGCTimeStart:  first.GCT,
		TotalGCTimeEnd:    last.GCT,
		OldUsedDeltaKiB:   last.OU - first.OU,
		MetaspaceDeltaKiB: last.MU - first.MU,
		FullGCDelta:       last.FGC - first.FGC,
		TotalGCTimeDelta:  last.GCT - first.GCT,
	}
}

func explainTrend(e *Explanation, trend *ResourceTrend) {
	if trend == nil || trend.Samples < 2 {
		return
	}
	severity := "info"
	summary := "Repeated GC samples did not show enough evidence for a JVM memory leak."
	if trend.MetaspaceDeltaKiB > 1024 {
		severity = "warning"
		summary = "Metaspace increased during the sample window."
	}
	if trend.OldUsedDeltaKiB > 10240 && trend.FullGCDelta > 0 {
		severity = "warning"
		summary = "Old generation increased despite full GC activity during the sample window."
	}
	e.Observations = append(e.Observations, Observation{
		ID:       "resource-trend",
		Severity: severity,
		Summary:  summary,
		Evidence: fmt.Sprintf("old %+0.f KiB, metaspace %+0.f KiB, full GC %+d, GC time %+0.3fs across %d samples", trend.OldUsedDeltaKiB, trend.MetaspaceDeltaKiB, trend.FullGCDelta, trend.TotalGCTimeDelta, trend.Samples),
	})
}

type metaspaceUsage struct {
	loaders      int
	classes      int
	usedMiB      float64
	committedMiB float64
	usedPct      float64
}

var metaspaceHeaderRE = regexp.MustCompile(`Total Usage - ([0-9]+) loaders, ([0-9]+) classes`)
var bothUsageRE = regexp.MustCompile(`Both:.*?([0-9.]+) MB[^,\n]*committed,\s+([0-9.]+) MB[^,\n]*used`)

func parseMetaspaceUsage(out string) metaspaceUsage {
	var usage metaspaceUsage
	if m := metaspaceHeaderRE.FindStringSubmatch(out); len(m) == 3 {
		usage.loaders, _ = strconv.Atoi(m[1])
		usage.classes, _ = strconv.Atoi(m[2])
	}
	if m := bothUsageRE.FindStringSubmatch(out); len(m) == 3 {
		usage.committedMiB, _ = strconv.ParseFloat(m[1], 64)
		usage.usedMiB, _ = strconv.ParseFloat(m[2], 64)
		if usage.committedMiB > 0 {
			usage.usedPct = usage.usedMiB * 100 / usage.committedMiB
		}
	}
	return usage
}

type classloaderTotal struct {
	loaders    int
	classes    int
	chunkBytes int64
	blockBytes int64
}

func parseClassLoaderTotal(out string) classloaderTotal {
	var total classloaderTotal
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 6 && fields[0] == "Total" && fields[1] == "=" {
			total.loaders, _ = strconv.Atoi(fields[2])
			total.classes, _ = strconv.Atoi(fields[3])
			total.chunkBytes, _ = strconv.ParseInt(fields[4], 10, 64)
			total.blockBytes, _ = strconv.ParseInt(fields[5], 10, 64)
			return total
		}
	}
	return total
}

type codeCacheUse struct {
	sizeKiB   float64
	usedKiB   float64
	usedPct   float64
	nmethods  int
	adapters  int
	fullCount int
}

var codeCacheRE = regexp.MustCompile(`CodeCache: size=([0-9.]+)Kb, used=([0-9.]+)Kb`)

func parseCodeCache(out string) codeCacheUse {
	var use codeCacheUse
	if m := codeCacheRE.FindStringSubmatch(out); len(m) == 3 {
		use.sizeKiB, _ = strconv.ParseFloat(m[1], 64)
		use.usedKiB, _ = strconv.ParseFloat(m[2], 64)
		if use.sizeKiB > 0 {
			use.usedPct = use.usedKiB * 100 / use.sizeKiB
		}
	}
	for _, line := range strings.Split(out, "\n") {
		for _, field := range strings.Fields(line) {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			v = strings.TrimRight(v, ",")
			switch k {
			case "nmethods":
				use.nmethods, _ = strconv.Atoi(v)
			case "adapters":
				use.adapters, _ = strconv.Atoi(v)
			case "full_count":
				use.fullCount, _ = strconv.Atoi(v)
			}
		}
	}
	return use
}

func parseSoftReferences(out string) SoftReferenceUse {
	var refs SoftReferenceUse
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if fields[len(fields)-1] != "java.lang.ref.SoftReference" {
			continue
		}
		instances, _ := strconv.ParseInt(fields[1], 10, 64)
		bytes, _ := strconv.ParseInt(fields[2], 10, 64)
		refs.Instances += instances
		refs.Bytes += bytes
	}
	return refs
}

func parseMemoryArgMiB(args, prefix string) float64 {
	for _, field := range strings.Fields(args) {
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		return memoryValueMiB(strings.TrimPrefix(field, prefix))
	}
	return 0
}

func parseXXNumberArg(args, name string) float64 {
	prefix := "-XX:" + name + "="
	for _, field := range strings.Fields(args) {
		if strings.HasPrefix(field, prefix) {
			n, err := strconv.ParseFloat(strings.TrimPrefix(field, prefix), 64)
			if err == nil {
				return n
			}
		}
	}
	return -1
}

func memoryValueMiB(raw string) float64 {
	if raw == "" {
		return 0
	}
	unit := raw[len(raw)-1]
	value := raw
	mult := 1.0
	switch unit {
	case 'k', 'K':
		value = raw[:len(raw)-1]
		mult = 1.0 / 1024
	case 'm', 'M':
		value = raw[:len(raw)-1]
	case 'g', 'G':
		value = raw[:len(raw)-1]
		mult = 1024
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return n * mult
}
