package rules

import (
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// SnapshotActivation projects snapshot.Snapshot into the stable rule data
// model used by CEL/YAML catalogs. This projection is the compatibility
// boundary for external rules; raw Go structs remain an implementation detail.
func SnapshotActivation(s snapshot.Snapshot) map[string]any {
	return map[string]any{
		"snapshot": map[string]any{
			"id":       s.ID,
			"taken_at": s.TakenAt.Format("2006-01-02T15:04:05Z07:00"),
			"kind":     string(s.Kind),
		},
		"host":       projectHost(s.Host),
		"apps":       projectApps(s.Apps),
		"processes":  projectProcesses(s.Processes),
		"jvms":       projectJVMs(s.JVMs, s.JVMHistory),
		"toolchains": projectToolchains(s.Toolchains),
		"network":    s.Network,
		"storage":    s.Storage,
		"power":      s.Power,
		"sysctls":    s.Sysctls,
		"fd_limit": map[string]any{
			"soft":           s.FDLimit.Soft,
			"hard":           s.FDLimit.Hard,
			"hard_unlimited": s.FDLimit.HardUnlimited,
		},
	}
}

func projectHost(h snapshot.HostInfo) map[string]any {
	return map[string]any{
		"hostname":        h.Hostname,
		"machine_uuid":    h.MachineUUID,
		"os_name":         h.OSName,
		"os_version":      h.OSVersion,
		"os_build":        h.OSBuild,
		"cpu_brand":       h.CPUBrand,
		"cpu_cores":       h.CPUCores,
		"ram_bytes":       h.RAMBytes,
		"ram_mb":          h.RAMBytes / 1024 / 1024,
		"architecture":    h.Architecture,
		"uptime_seconds":  h.UptimeSeconds,
		"spectra_version": h.SpectraVersion,
	}
}

func projectApps(apps []detect.Result) []any {
	out := make([]any, 0, len(apps))
	for _, app := range apps {
		out = append(out, map[string]any{
			"path":                 app.Path,
			"bundle_id":            app.BundleID,
			"version":              app.AppVersion,
			"build_number":         app.BuildNumber,
			"ui":                   app.UI,
			"runtime":              app.Runtime,
			"language":             app.Language,
			"packaging":            app.Packaging,
			"confidence":           app.Confidence,
			"architectures":        app.Architectures,
			"bundle_size_bytes":    app.BundleSizeBytes,
			"apparent_size_bytes":  app.ApparentSizeBytes,
			"team_id":              app.TeamID,
			"hardened_runtime":     app.HardenedRuntime,
			"sandboxed":            app.Sandboxed,
			"entitlements":         app.Entitlements,
			"granted_permissions":  app.GrantedPermissions,
			"privacy_descriptions": app.PrivacyDescriptions,
			"gatekeeper_status":    app.GatekeeperStatus,
			"storage_total_bytes":  appStorageTotal(app),
			"login_items":          app.LoginItems,
			"network_endpoints":    app.NetworkEndpoints,
		})
	}
	return out
}

func appStorageTotal(app detect.Result) int64 {
	if app.Storage == nil {
		return 0
	}
	return app.Storage.Total
}

func projectProcesses(processes []process.Info) []any {
	out := make([]any, 0, len(processes))
	for _, proc := range processes {
		out = append(out, map[string]any{
			"pid":                  proc.PID,
			"ppid":                 proc.PPID,
			"uid":                  proc.UID,
			"user":                 proc.User,
			"command":              proc.Command,
			"full_command_line":    proc.FullCommandLine,
			"rss_kib":              proc.RSSKiB,
			"vsize_kib":            proc.VSizeKiB,
			"thread_count":         proc.ThreadCount,
			"cpu_pct":              proc.CPUPct,
			"app_path":             proc.AppPath,
			"open_fds":             proc.OpenFDs,
			"listening_ports":      proc.ListeningPorts,
			"outbound_connections": proc.OutboundConnections,
		})
	}
	return out
}

func projectJVMs(jvms []jvm.Info, history snapshot.JVMHistory) []any {
	out := make([]any, 0, len(jvms))
	for _, info := range jvms {
		out = append(out, map[string]any{
			"pid":            info.PID,
			"main_class":     info.MainClass,
			"java_home":      info.JavaHome,
			"jdk_vendor":     info.JDKVendor,
			"jdk_version":    info.JDKVersion,
			"version_major":  parseMajor(info.JDKVersion),
			"jdk_install_id": info.JDKInstallID,
			"jdk_source":     info.JDKSource,
			"jdk_path":       info.JDKPath,
			"vm_args":        info.VMArgs,
			"max_heap_mb":    parseXmxMB(info.VMArgs),
			"vm_flags":       info.VMFlags,
			"thread_count":   info.ThreadCount,
			"sys_props":      info.SysProps,
			"gc_count":       gcCount(info.GC),
			"gc":             projectGC(info.GC),
			"classes":        projectClasses(info.Classes),
			"history":        projectJVMHistory(history, info.PID),
		})
	}
	return out
}

// gcCount is the total GC event count (young + full) documented as
// jvm.gc_count, or 0 when GC stats were not collected.
func gcCount(gc *jvm.GCStats) int64 {
	if gc == nil {
		return 0
	}
	return gc.YGC + gc.FGC
}

// projectGC exposes the one-shot jstat GC counters to external rules, or nil
// when GC stats were not collected.
func projectGC(gc *jvm.GCStats) any {
	if gc == nil {
		return nil
	}
	m := map[string]any{
		"ygc":  gc.YGC,
		"ygct": gc.YGCT,
		"fgc":  gc.FGC,
		"fgct": gc.FGCT,
		"gct":  gc.GCT,
		"s0c":  gc.S0C,
		"s1c":  gc.S1C,
		"s0u":  gc.S0U,
		"s1u":  gc.S1U,
		"ec":   gc.EC,
		"eu":   gc.EU,
		"oc":   gc.OC,
		"ou":   gc.OU,
		"mc":   gc.MC,
		"mu":   gc.MU,
		"ccsc": gc.CCSC,
		"ccsu": gc.CCSU,
		// Convenience aliases so external rules read the same names the
		// built-in Go rules use.
		"full_gc_count":  gc.FGC,
		"full_gc_time_s": gc.FGCT,
		"young_gc_count": gc.YGC,
	}
	// Derived occupancy percentages. Kept identical to the predicate math in
	// predicates.go (OldGenUsedPct / metaspace) so external and built-in rules
	// agree on the same thresholds.
	if gc.OC > 0 {
		m["old_gen_used_pct"] = gc.OU * 100 / gc.OC
	} else {
		m["old_gen_used_pct"] = 0.0
	}
	if gc.MC > 0 {
		m["metaspace_used_pct"] = gc.MU * 100 / gc.MC
	} else {
		m["metaspace_used_pct"] = 0.0
	}
	return m
}

// projectClasses exposes the jstat -class counters to external rules.
func projectClasses(c *jvm.ClassStats) any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"loaded":          c.Loaded,
		"unloaded":        c.Unloaded,
		"loaded_kib":      c.LoadedKiB,
		"unloaded_kib":    c.UnloadedKiB,
		"class_load_time": c.ClassLoadTime,
	}
}

// projectJVMHistory summarizes the per-PID trend samples so a rule can gate on
// a rising heap without walking a raw sample slice in CEL.
//
// Unlike gc/classes (which are nil when the reading was not collected), history
// is ALWAYS a map with the same key set — zero-valued when there are no samples
// for the PID. This keeps `item.history.rising_old_gen` null-safe in CEL and
// lets a rule require data explicitly with `item.history.sample_count > 0`.
func projectJVMHistory(history snapshot.JVMHistory, pid int) map[string]any {
	samples := history.SamplesFor(pid)
	m := map[string]any{
		"sample_count":      len(samples),
		"has_trend":         HasTrendFor(history, pid),
		"rising_old_gen":    RisingOldGenFor(history, pid),
		"old_gen_pct_first": 0.0,
		"old_gen_pct_last":  0.0,
		"fgc_first":         int64(0),
		"fgc_last":          int64(0),
		"heap_mb_first":     int64(0),
		"heap_mb_last":      int64(0),
	}
	if len(samples) > 0 {
		first, last := samples[0], samples[len(samples)-1]
		m["old_gen_pct_first"] = first.OldGenPct
		m["old_gen_pct_last"] = last.OldGenPct
		m["fgc_first"] = first.FGC
		m["fgc_last"] = last.FGC
		m["heap_mb_first"] = first.HeapMB
		m["heap_mb_last"] = last.HeapMB
	}
	return m
}

func projectToolchains(t toolchain.Toolchains) map[string]any {
	return map[string]any{
		"brew":               t.Brew,
		"jdks":               projectJDKs(t.JDKs),
		"node":               t.Node,
		"python":             t.Python,
		"go":                 t.Go,
		"ruby":               t.Ruby,
		"rust":               t.Rust,
		"jvm_managers":       t.JVMManagers,
		"active_jvm_manager": t.ActiveJVMManager,
		"build_tools":        t.BuildTools,
		"env":                t.Env,
	}
}

func projectJDKs(jdks []toolchain.JDKInstall) []any {
	out := make([]any, 0, len(jdks))
	for _, jdk := range jdks {
		out = append(out, map[string]any{
			"install_id":          jdk.InstallID,
			"path":                jdk.Path,
			"source":              jdk.Source,
			"version_major":       jdk.VersionMajor,
			"version_minor":       jdk.VersionMinor,
			"version_patch":       jdk.VersionPatch,
			"vendor":              jdk.Vendor,
			"release_string":      jdk.ReleaseString,
			"is_active_java_home": jdk.IsActiveJavaHome,
		})
	}
	return out
}
