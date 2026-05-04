package jvm

import (
	"context"
	"fmt"
	"sync"
)

// CollectOptions configure the JVM collector.
type CollectOptions struct {
	// CmdRunner is used for all subprocess calls. Nil means DefaultRunner.
	CmdRunner CmdRunner
}

// CollectAll discovers all running JVM processes and collects per-process
// metadata via jcmd. Returns nil if jps is not on PATH (no JDK installed).
// Any per-process collection failure is silently absorbed.
func CollectAll(ctx context.Context, opts CollectOptions) []Info {
	run := opts.CmdRunner
	if run == nil {
		run = DefaultRunner
	}

	pids := discoverPIDs(run)
	if len(pids) == 0 {
		return nil
	}

	results := make([]Info, 0, len(pids))
	var mu sync.Mutex
	var wg sync.WaitGroup

	for pid, main := range pids {
		pid, main := pid, main
		wg.Add(1)
		go func() {
			defer wg.Done()
			info := collectOne(ctx, pid, main, run)
			mu.Lock()
			results = append(results, info)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// InspectPID collects Info for a specific PID without running jps.
// Returns nil if jcmd is not available or the process doesn't exist.
func InspectPID(_ context.Context, pid int, opts CollectOptions) *Info {
	run := opts.CmdRunner
	if run == nil {
		run = DefaultRunner
	}
	// Quick sanity check: jcmd with the PID; if it errors the process is gone.
	if _, err := run("jcmd", fmt.Sprint(pid), "VM.version"); err != nil {
		return nil
	}
	info := collectOne(context.Background(), pid, "", run)
	return &info
}

// collectOne gathers Info for a single PID.
func collectOne(_ context.Context, pid int, main string, run CmdRunner) Info {
	info := Info{PID: pid, MainClass: main}

	props := collectSysProps(pid, run)
	info.SysProps = props
	if props != nil {
		info.JavaHome = props["java.home"]
		info.JDKVendor = props["java.vendor"]
		info.JDKVersion = props["java.version"]
	}

	info.VMFlags, info.VMArgs = collectCommandLine(pid, run)
	info.ThreadCount = collectThreadCount(pid, run)

	return info
}
