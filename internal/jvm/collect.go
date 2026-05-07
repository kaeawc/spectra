package jvm

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/kaeawc/spectra/internal/toolchain"
)

// CollectOptions configure the JVM collector.
type CollectOptions struct {
	// CmdRunner is used for all subprocess calls. Nil means DefaultRunner.
	CmdRunner CmdRunner
	// JDKs is the already-discovered JDK inventory used to attribute each
	// running JVM's java.home back to an installed toolchain.
	JDKs []toolchain.JDKInstall
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
			info := collectOne(ctx, pid, main, run, opts.JDKs)
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
	info := collectOne(context.Background(), pid, "", run, opts.JDKs)
	return &info
}

// AttributeJDKs attaches installed-JDK identity fields to already-collected
// JVM process snapshots when java.home matches a discovered JDK path.
func AttributeJDKs(infos []Info, jdks []toolchain.JDKInstall) {
	for i := range infos {
		attributeJDK(&infos[i], jdks)
	}
}

// collectOne gathers Info for a single PID.
func collectOne(_ context.Context, pid int, main string, run CmdRunner, jdks []toolchain.JDKInstall) Info {
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
	if gc, err := CollectGCStats(pid, run); err == nil {
		info.GC = gc
	}
	if classes, err := CollectClassStats(pid, run); err == nil {
		info.Classes = classes
	}
	attributeJDK(&info, jdks)

	return info
}

func attributeJDK(info *Info, jdks []toolchain.JDKInstall) {
	if info == nil || info.JavaHome == "" || len(jdks) == 0 {
		return
	}
	javaHome := filepath.Clean(info.JavaHome)
	for _, install := range jdks {
		if install.Path == "" {
			continue
		}
		if filepath.Clean(install.Path) != javaHome {
			continue
		}
		info.JDKInstallID = install.InstallID
		info.JDKSource = install.Source
		info.JDKPath = install.Path
		return
	}
}
