package snapshot

import (
	"context"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/telemetry"
	"github.com/kaeawc/spectra/internal/toolchain"
)

func collectRuntimeTelemetry(ctx context.Context, collectors []telemetry.Collector) []telemetry.Process {
	var out []telemetry.Process
	for _, collector := range collectors {
		if collector == nil {
			continue
		}
		out = append(out, collector.CollectRuntimeTelemetry(ctx)...)
	}
	return out
}

func collectJVMTelemetry(_ context.Context, infos []jvm.Info, opts Options) []telemetry.Process {
	if len(infos) == 0 {
		return nil
	}
	jvmOpts := opts.JVMTelemetryOpts
	if jvmOpts.CmdRunner == nil {
		jvmOpts.CmdRunner = opts.JVMOpts.CmdRunner
	}
	if len(jvmOpts.JDKs) == 0 {
		jvmOpts.JDKs = opts.JVMOpts.JDKs
	}
	out := make([]telemetry.Process, 0, len(infos))
	for _, info := range infos {
		out = append(out, jvm.TelemetryForInfo(info, jvmOpts))
	}
	return out
}

func attributeRuntimeJDKs(processes []telemetry.Process, jdks []toolchain.JDKInstall) {
	if len(jdks) == 0 {
		return
	}
	for i := range processes {
		if processes[i].Runtime != telemetry.RuntimeJVM {
			continue
		}
		identity := processes[i].Identity
		if identity == nil || identity["java_home"] == "" || identity["jdk_install_id"] != "" {
			continue
		}
		javaHome := filepath.Clean(identity["java_home"])
		for _, install := range jdks {
			if install.Path == "" || filepath.Clean(install.Path) != javaHome {
				continue
			}
			identity["jdk_install_id"] = install.InstallID
			identity["jdk_source"] = install.Source
			identity["jdk_path"] = install.Path
			break
		}
	}
}
