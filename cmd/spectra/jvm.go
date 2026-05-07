package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/diag"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/toolchain"
)

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runJVM(args []string) int {
	// Dispatch subcommands before flag parsing so "--help" on a subcommand works.
	if handler, ok := resolveJVMSubcommand(args); ok {
		return handler(args[1:])
	}

	fs := flag.NewFlagSet("spectra jvm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	jvmOpts := jvmOptions(ctx)
	var infos []jvm.Info

	if fs.NArg() == 0 {
		infos = jvm.CollectAll(ctx, jvmOpts)
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, "no running JVMs found (is jps in your PATH?)")
			return 0
		}
	} else {
		pidStr := fs.Arg(0)
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid PID %q\n", pidStr)
			return 2
		}
		info := jvm.InspectPID(ctx, pid, jvmOpts)
		if info == nil {
			fmt.Fprintf(os.Stderr, "could not inspect PID %d (process not found or jcmd unavailable)\n", pid)
			return 1
		}
		infos = []jvm.Info{*info}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(infos)
		return 0
	}

	if fs.NArg() == 0 {
		printJVMList(infos)
	} else {
		printJVMDetail(infos[0])
	}
	return 0
}

func resolveJVMSubcommand(args []string) (func([]string) int, bool) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return nil, false
	}
	handlers := map[string]func([]string) int{
		"thread-dump":    runJVMThreadDump,
		"heap-histogram": runJVMHeapHistogram,
		"heap-dump":      runJVMHeapDump,
		"jfr":            runJVMJFR,
		"gc-stats":       runJVMGCStats,
		"vm-memory":      runJVMVMMemory,
		"jmx":            runJVMJMX,
		"attach":         runJVMAttach,
		"mbeans":         runJVMMBeans,
		"mbean-read":     runJVMMBeanRead,
		"mbean-invoke":   runJVMMBeanInvoke,
		"probe":          runJVMProbe,
		"flamegraph":     runJVMFlamegraph,
		"explain":        runJVMExplain,
	}
	handler, ok := handlers[args[0]]
	return handler, ok
}

func jvmOptions(ctx context.Context) jvm.CollectOptions {
	return jvm.CollectOptions{JDKs: toolchain.CollectJDKs(ctx, toolchain.CollectOptions{})}
}

func runJVMThreadDump(args []string) int {
	fs := flag.NewFlagSet("spectra jvm thread-dump", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm thread-dump <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	data, err := jvm.ThreadDump(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "thread-dump failed for PID %d: %v\n", pid, err)
		return 1
	}

	key := cache.Key([]byte(fmt.Sprintf("threads:%d:%d", pid, time.Now().UnixNano())))
	if cacheStores != nil && cacheStores.Threads != nil {
		if putErr := cacheStores.Threads.Put(key, data); putErr == nil {
			fmt.Fprintf(os.Stderr, "cached as threads/%x\n", key[:4])
		}
	}
	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindThreadDump,
		Sensitivity: artifact.SensitivityMediumHigh,
		Source:      "cli",
		Command:     "spectra jvm thread-dump",
		CacheKind:   cache.KindThreads,
		PID:         pid,
		SizeBytes:   int64(len(data)),
	})

	os.Stdout.Write(data)
	return 0
}

func runJVMHeapHistogram(args []string) int {
	fs := flag.NewFlagSet("spectra jvm heap-histogram", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm heap-histogram <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	data, err := jvm.HeapHistogram(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heap-histogram failed for PID %d: %v\n", pid, err)
		return 1
	}

	os.Stdout.Write(data)
	return 0
}

func runJVMHeapDump(args []string) int {
	fs := flag.NewFlagSet("spectra jvm heap-dump", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("out", "", "Output .hprof path (default: ~/.spectra/<pid>-<ts>.hprof)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm heap-dump [--out <path>] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	destPath := *out
	if destPath == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			fmt.Fprintln(os.Stderr, herr)
			return 1
		}
		ts := time.Now().UTC().Format("20060102T150405Z")
		destPath = filepath.Join(home, ".spectra", fmt.Sprintf("%d-%s.hprof", pid, ts))
		if mkErr := os.MkdirAll(filepath.Dir(destPath), 0o700); mkErr != nil {
			fmt.Fprintln(os.Stderr, mkErr)
			return 1
		}
	}

	fmt.Fprintf(os.Stderr, "writing heap dump to %s ...\n", destPath)
	if err := jvm.HeapDump(pid, destPath, nil); err != nil {
		fmt.Fprintf(os.Stderr, "heap-dump failed for PID %d: %v\n", pid, err)
		return 1
	}
	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindHeapDump,
		Sensitivity: artifact.SensitivityVeryHigh,
		Source:      "cli",
		Command:     "spectra jvm heap-dump",
		Path:        destPath,
		CacheKind:   cache.KindHprof,
		PID:         pid,
	})
	fmt.Println(destPath)
	return 0
}

func runJVMGCStats(args []string) int {
	fs := flag.NewFlagSet("spectra jvm gc-stats", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm gc-stats [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	stats, err := jvm.CollectGCStats(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gc-stats failed for PID %d: %v\n", pid, err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(stats)
		return 0
	}

	fmt.Printf("GC stats for PID %d\n", pid)
	fmt.Printf("  Young GC:  %d events, %.3fs total\n", stats.YGC, stats.YGCT)
	fmt.Printf("  Full GC:   %d events, %.3fs total\n", stats.FGC, stats.FGCT)
	fmt.Printf("  Total GC:  %.3fs\n", stats.GCT)
	fmt.Printf("  Eden:      %.0f KiB used / %.0f KiB capacity\n", stats.EU, stats.EC)
	fmt.Printf("  Old:       %.0f KiB used / %.0f KiB capacity\n", stats.OU, stats.OC)
	fmt.Printf("  Metaspace: %.0f KiB used / %.0f KiB capacity\n", stats.MU, stats.MC)
	return 0
}

func runJVMVMMemory(args []string) int {
	fs := flag.NewFlagSet("spectra jvm vm-memory", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm vm-memory [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	diagnostics := jvm.CollectVMMemoryDiagnostics(pid, nil)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(diagnostics)
		return 0
	}

	fmt.Printf("VM memory diagnostics for PID %d\n", pid)
	printDiagnosticSection("Heap", diagnostics.HeapInfo)
	printDiagnosticSection("Metaspace", diagnostics.Metaspace)
	printDiagnosticSection("Native memory", diagnostics.NativeMemory)
	printDiagnosticSection("Class loaders", diagnostics.ClassLoaderStats)
	printDiagnosticSection("Code cache", diagnostics.CodeCache)
	printDiagnosticSection("Code heap", diagnostics.CodeHeap)
	return 0
}

func runJVMJMX(args []string) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm jmx <status|start-local> [--json] <pid>")
		return 2
	}
	sub := args[0]
	fs := flag.NewFlagSet("spectra jvm jmx "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: spectra jvm jmx %s [--json] <pid>\n", sub)
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	var out []byte
	switch sub {
	case "status":
		out, err = jvm.JMXStatus(pid, nil)
	case "start-local":
		out, err = jvm.JMXStartLocal(pid, nil)
	default:
		fmt.Fprintf(os.Stderr, "unknown jmx subcommand %q; use status or start-local\n", sub)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "jmx %s failed for PID %d: %v\n", sub, pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"pid": pid, "output": string(out)})
		return 0
	}
	os.Stdout.Write(out)
	return 0
}

func runJVMAttach(args []string) int {
	fs := flag.NewFlagSet("spectra jvm attach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jar := fs.String("agent", "", "Path to spectra-agent.jar (default: SPECTRA_AGENT_JAR, binary dir, or ./agent/spectra-agent.jar)")
	transport := fs.String("transport", "http", "Agent transport: http or unix")
	socket := fs.String("socket", "", "Unix socket path when --transport unix (default: target JVM temp dir)")
	var counters stringListFlag
	var workflows stringListFlag
	fs.Var(&counters, "counter", "Named counter as name=object-name:attribute; repeatable")
	fs.Var(&workflows, "workflow", "Workflow probe as name=counter=object-name:attribute[+...]; repeatable")
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm attach [--agent <spectra-agent.jar>] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	if *transport != "http" && *transport != "unix" {
		fmt.Fprintln(os.Stderr, "--transport must be http or unix")
		return 2
	}
	status, err := jvm.AttachAgentWithOptions(pid, jvm.AttachOptions{
		JarPath:   *jar,
		Transport: *transport,
		Socket:    *socket,
		Counters:  counters,
		Workflows: workflows,
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "attach failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(status)
		return 0
	}
	if status.Transport == "unix" {
		fmt.Printf("spectra agent attached to PID %d on %s\n", status.PID, status.Socket)
	} else {
		fmt.Printf("spectra agent attached to PID %d on 127.0.0.1:%d\n", status.PID, status.Port)
	}
	return 0
}

func runJVMMBeans(args []string) int {
	fs := flag.NewFlagSet("spectra jvm mbeans", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm mbeans [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	result, err := jvm.FetchMBeans(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mbeans failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printMBeans(result)
	return 0
}

func runJVMMBeanRead(args []string) int {
	fs := flag.NewFlagSet("spectra jvm mbean-read", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 3 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm mbean-read [--json] <pid> <object-name> <attribute>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	result, err := jvm.ReadMBeanAttribute(pid, fs.Arg(1), fs.Arg(2), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mbean-read failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printMBeanAttribute(result)
	return 0
}

func runJVMMBeanInvoke(args []string) int {
	fs := flag.NewFlagSet("spectra jvm mbean-invoke", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 3 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm mbean-invoke [--json] <pid> <object-name> <zero-arg-operation>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	result, err := jvm.InvokeMBeanOperation(pid, fs.Arg(1), fs.Arg(2), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mbean-invoke failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printMBeanInvocation(result)
	return 0
}

func runJVMProbe(args []string) int {
	fs := flag.NewFlagSet("spectra jvm probe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm probe [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	probes, err := jvm.FetchAgentProbes(pid, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(probes)
		return 0
	}
	fmt.Printf("Runtime processors  %d\n", probes.Runtime.AvailableProcessors)
	fmt.Printf("Heap free/total/max  %d / %d / %d bytes\n", probes.Runtime.FreeMemory, probes.Runtime.TotalMemory, probes.Runtime.MaxMemory)
	fmt.Printf("Live threads         %d\n", probes.Threads.Live)
	for _, counter := range probes.Counters {
		if counter.Error != "" {
			fmt.Printf("Counter %-14s error: %s\n", counter.Name, counter.Error)
			continue
		}
		fmt.Printf("Counter %-14s %v\n", counter.Name, counter.Value)
	}
	for _, workflow := range probes.Workflows {
		fmt.Printf("Workflow %s\n", workflow.Name)
		for _, counter := range workflow.Counters {
			if counter.Error != "" {
				fmt.Printf("  %-16s error: %s\n", counter.Name, counter.Error)
				continue
			}
			fmt.Printf("  %-16s %v\n", counter.Name, counter.Value)
		}
	}
	return 0
}

func runJVMFlamegraph(args []string) int {
	fs := flag.NewFlagSet("spectra jvm flamegraph", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	event := fs.String("event", "cpu", "async-profiler event: cpu, wall, alloc, lock")
	duration := fs.Int("duration", 30, "Capture duration in seconds")
	out := fs.String("out", "", "Output flamegraph path (default: ~/.spectra/<pid>-<ts>-<event>.html)")
	asprof := fs.String("asprof", "asprof", "async-profiler CLI path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm flamegraph [--event cpu] [--duration 30] [--out <path>] [--asprof asprof] <pid>")
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "--duration must be greater than zero")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	dest := *out
	if dest == "" {
		var err error
		dest, err = defaultFlamegraphPath(pid, *event)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := jvm.CaptureFlamegraph(pid, jvm.FlamegraphOptions{
		AsprofPath:      *asprof,
		Event:           *event,
		DurationSeconds: *duration,
		OutputPath:      dest,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flamegraph failed for PID %d: %v\n", pid, err)
		return 1
	}
	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindFlamegraph,
		Sensitivity: artifact.SensitivityMediumHigh,
		Source:      "cli",
		Command:     "spectra jvm flamegraph",
		Path:        dest,
		PID:         pid,
		Metadata: map[string]string{
			"event": *event,
		},
	})
	fmt.Println(dest)
	return 0
}

func runJVMExplain(args []string) int {
	fs := flag.NewFlagSet("spectra jvm explain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	samples := fs.Int("samples", 1, "Number of jstat samples for trend analysis")
	interval := fs.Duration("interval", 1*time.Second, "Interval between trend samples")
	softRefs := fs.Bool("soft-refs", true, "Check java.lang.ref.SoftReference instances with a class histogram")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm explain [--json] [--samples 1] [--interval 1s] [--soft-refs=true] <pid>")
		return 2
	}
	if *samples <= 0 {
		fmt.Fprintln(os.Stderr, "--samples must be greater than zero")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	explanation, err := jvm.CollectExplanation(context.Background(), pid, jvm.ExplainOptions{
		Samples:  *samples,
		Interval: *interval,
		SoftRefs: *softRefs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "explain failed for PID %d: %v\n", pid, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(explanation)
		return 0
	}
	printJVMExplanation(*explanation)
	return 0
}

func runJVMJFR(args []string) int {
	if len(args) > 0 && args[0] == "summary" {
		return runJFRSummary(args[1:])
	}
	sub, pid, name, outPath, code := parseJFRArgs(args)
	if code != 0 {
		return code
	}
	return executeJFR(sub, pid, name, outPath)
}

func runJFRSummary(args []string) int {
	fs := flag.NewFlagSet("spectra jvm jfr summary", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm jfr summary [--json] <recording.jfr>")
		return 2
	}

	summary, err := jvm.SummarizeJFR(fs.Arg(0), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "jfr summary failed for %s: %v\n", fs.Arg(0), err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}
	printJFRSummary(summary)
	return 0
}

func parseJFRArgs(args []string) (string, int, string, string, int) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm jfr <start|dump|stop> <pid> [--out <path>] [--name <name>]")
		return "", 0, "", "", 2
	}
	sub := args[0]
	rest := args[1:]

	fs := flag.NewFlagSet("spectra jvm jfr "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	outPath := fs.String("out", "", "Output .jfr path (for dump/stop)")
	name := fs.String("name", "spectra", "Recording name")
	if err := fs.Parse(rest); err != nil {
		return "", 0, "", "", 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: spectra jvm jfr %s [flags] <pid>\n", sub)
		return "", 0, "", "", 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return "", 0, "", "", 2
	}
	return sub, pid, *name, *outPath, 0
}

func executeJFR(sub string, pid int, name, outPath string) int {
	switch sub {
	case "start":
		return runJFRStart(pid, name)
	case "dump":
		return runJFRDump(pid, name, outPath)
	case "stop":
		return runJFRStop(pid, name, outPath)
	default:
		fmt.Fprintf(os.Stderr, "unknown jfr subcommand %q; use start, dump, or stop\n", sub)
		return 2
	}
}

func runJFRStart(pid int, name string) int {
	if err := jvm.JFRStart(pid, name, nil); err != nil {
		fmt.Fprintf(os.Stderr, "JFR.start failed for PID %d: %v\n", pid, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "JFR recording %q started on PID %d\n", name, pid)
	return 0
}

func runJFRDump(pid int, name, dest string) int {
	if dest == "" {
		var err error
		dest, err = defaultJFRPath(pid)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if err := jvm.JFRDump(pid, name, dest, nil); err != nil {
		fmt.Fprintf(os.Stderr, "JFR.dump failed for PID %d: %v\n", pid, err)
		return 1
	}
	cacheJFRDump(pid, dest)
	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindJFRRecording,
		Sensitivity: artifact.SensitivityMediumHigh,
		Source:      "cli",
		Command:     "spectra jvm jfr dump",
		Path:        dest,
		CacheKind:   cache.KindJFR,
		PID:         pid,
		Metadata: map[string]string{
			"name": name,
		},
	})
	fmt.Println(dest)
	return 0
}

func defaultFlamegraphPath(pid int, event string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	dest := filepath.Join(home, ".spectra", fmt.Sprintf("%d-%s-%s.html", pid, ts, event))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	return dest, nil
}

func defaultJFRPath(pid int) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	dest := filepath.Join(home, ".spectra", fmt.Sprintf("%d-%s.jfr", pid, ts))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	return dest, nil
}

func cacheJFRDump(pid int, dest string) {
	if cacheStores == nil || cacheStores.JFR == nil {
		return
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		return
	}
	key := cache.Key([]byte(fmt.Sprintf("jfr:%d:%d", pid, time.Now().UnixNano())))
	_ = cacheStores.JFR.Put(key, data)
}

func runJFRStop(pid int, name, dest string) int {
	if err := jvm.JFRStop(pid, name, dest, nil); err != nil {
		fmt.Fprintf(os.Stderr, "JFR.stop failed for PID %d: %v\n", pid, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "JFR recording %q stopped on PID %d\n", name, pid)
	if dest != "" {
		recordArtifactCLI(artifact.Record{
			Kind:        artifact.KindJFRRecording,
			Sensitivity: artifact.SensitivityMediumHigh,
			Source:      "cli",
			Command:     "spectra jvm jfr stop",
			Path:        dest,
			CacheKind:   cache.KindJFR,
			PID:         pid,
			Metadata: map[string]string{
				"name": name,
			},
		})
		fmt.Println(dest)
	}
	return 0
}

func printJFRSummary(summary jvm.JFRSummary) {
	fmt.Printf("File      %s\n", strOrDash(summary.Path))
	fmt.Printf("Version   %s\n", strOrDash(summary.Version))
	fmt.Printf("Chunks    %s\n", intOrDash(summary.Chunks))
	fmt.Printf("Start     %s\n", strOrDash(summary.Start))
	fmt.Printf("Duration  %s\n", strOrDash(summary.Duration))
	if len(summary.Events) == 0 {
		return
	}
	fmt.Println("\nEvents:")
	fmt.Printf("%-48s %10s %12s\n", "TYPE", "COUNT", "BYTES")
	fmt.Println(strings.Repeat("-", 74))
	for _, event := range summary.Events {
		fmt.Printf("%-48s %10d %12d\n", truncate(event.Type, 48), event.Count, event.SizeBytes)
	}
}

func printDiagnosticSection(title string, section jvm.DiagnosticSection) {
	fmt.Printf("\n%s\n", title)
	fmt.Printf("Command  %s\n", strings.Join(section.Command, " "))
	if section.Error != "" {
		fmt.Printf("Error    %s\n", section.Error)
		return
	}
	if section.Output == "" {
		fmt.Println("(no output)")
		return
	}
	fmt.Println(section.Output)
}

func printMBeanAttribute(result jvm.MBeanAttributeValue) {
	fmt.Printf("MBean      %s\n", result.MBean)
	fmt.Printf("Attribute  %s\n", result.Attribute)
	if result.Error != "" {
		fmt.Printf("Error      %s\n", result.Error)
		return
	}
	fmt.Printf("Type       %s\n", strOrDash(result.Type))
	fmt.Printf("Value      %v\n", result.Value)
}

func printMBeanInvocation(result jvm.MBeanInvocation) {
	fmt.Printf("MBean      %s\n", result.MBean)
	fmt.Printf("Operation  %s\n", result.Operation)
	if result.Error != "" {
		fmt.Printf("Error      %s\n", result.Error)
		return
	}
	fmt.Printf("Type       %s\n", strOrDash(result.Type))
	fmt.Printf("Value      %v\n", result.Value)
}

func printMBeans(result jvm.MBeansResult) {
	catalog := jvm.CatalogMBeans(result)
	fmt.Printf("MBeans: %d\n", len(result.MBeans))
	for _, group := range catalog.Groups {
		fmt.Printf("\n%s\n", group.Name)
		for _, component := range group.Components {
			fmt.Printf("  %s\n", component.ID)
			fmt.Printf("    kind        %s\n", strOrDash(component.Kind))
			fmt.Printf("    attributes  %d\n", len(component.Attributes))
			fmt.Printf("    operations  %d\n", len(component.Operations))
		}
	}
}

func printJVMExplanation(explanation jvm.Explanation) {
	fmt.Printf("JVM explanation for PID %d\n", explanation.PID)
	fmt.Printf("Main class    %s\n", strOrDash(explanation.MainClass))
	fmt.Printf("JDK           %s %s\n", strOrDash(explanation.JDKVendor), strOrDash(explanation.JDKVersion))
	fmt.Printf("Java home     %s\n", strOrDash(explanation.JavaHome))
	if explanation.Trend != nil {
		fmt.Printf("Trend         %d samples every %dms\n", explanation.Trend.Samples, explanation.Trend.IntervalMillis)
	}
	if explanation.SoftRefs != nil {
		fmt.Printf("Soft refs     %d instances, %d bytes\n", explanation.SoftRefs.Instances, explanation.SoftRefs.Bytes)
	}
	fmt.Println("\nObservations:")
	for _, obs := range explanation.Observations {
		fmt.Printf("- [%s] %s: %s\n", obs.Severity, obs.ID, obs.Summary)
		if obs.Evidence != "" {
			fmt.Printf("  Evidence: %s\n", obs.Evidence)
		}
		if obs.Recommendation != "" {
			fmt.Printf("  Next: %s\n", obs.Recommendation)
		}
	}
}

func printJVMList(infos []jvm.Info) {
	sort.Slice(infos, func(i, j int) bool { return infos[i].PID < infos[j].PID })

	fmt.Printf("%-7s  %-12s  %-8s  %s\n", "PID", "VERSION", "THREADS", "MAIN CLASS")
	fmt.Println(strings.Repeat("-", 70))
	for _, info := range infos {
		ver := info.JDKVersion
		if ver == "" {
			ver = "?"
		}
		threads := "-"
		if info.ThreadCount > 0 {
			threads = strconv.Itoa(info.ThreadCount)
		}
		mc := info.MainClass
		if mc == "" {
			mc = "(unknown)"
		}
		fmt.Printf("%-7d  %-12s  %-8s  %s\n", info.PID, truncate(ver, 12), threads, mc)
	}
}

func printJVMDetail(info jvm.Info) {
	fmt.Printf("PID           %d\n", info.PID)
	fmt.Printf("Main class    %s\n", strOrDash(info.MainClass))
	fmt.Printf("JDK vendor    %s\n", strOrDash(info.JDKVendor))
	fmt.Printf("JDK version   %s\n", strOrDash(info.JDKVersion))
	fmt.Printf("Posture       %s\n", strOrDash(info.Posture))
	fmt.Printf("Java home     %s\n", strOrDash(info.JavaHome))
	fmt.Printf("JDK install   %s\n", strOrDash(info.JDKInstallID))
	fmt.Printf("JDK source    %s\n", strOrDash(info.JDKSource))
	fmt.Printf("VM args       %s\n", strOrDash(info.VMArgs))
	fmt.Printf("VM flags      %s\n", strOrDash(info.VMFlags))
	fmt.Printf("Threads       %s\n", intOrDash(info.ThreadCount))
	printJVMDiagnosticCapabilities(info.Diagnostics)

	if len(info.SysProps) > 0 {
		fmt.Println("\nSystem properties:")
		keys := make([]string, 0, len(info.SysProps))
		for k := range info.SysProps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("  %-30s  %s\n", k, info.SysProps[k])
		}
	}
}

func printJVMDiagnosticCapabilities(matrix diag.Matrix) {
	if len(matrix.Capabilities) == 0 {
		return
	}
	fmt.Println("\nDiagnostics:")
	for _, capability := range matrix.Capabilities {
		fmt.Printf("  %-28s %-11s", capability.ID, capability.Status)
		if len(capability.Command) > 0 {
			fmt.Printf(" %s", strings.Join(capability.Command, " "))
		}
		fmt.Println()
	}
}

func strOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func intOrDash(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}
