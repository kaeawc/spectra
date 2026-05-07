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
		"jvms":       projectJVMs(s.JVMs),
		"toolchains": projectToolchains(s.Toolchains),
		"network":    s.Network,
		"storage":    s.Storage,
		"power":      s.Power,
		"sysctls":    s.Sysctls,
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

func projectJVMs(jvms []jvm.Info) []any {
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
		})
	}
	return out
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
