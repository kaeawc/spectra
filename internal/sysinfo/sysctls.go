// Package sysinfo collects selected kernel tunables (sysctls) and battery /
// power state. Both collectors are read-only, local, and user-privilege.
// See docs/design/system-inventory.md for the schema.
package sysinfo

import (
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
)

// AllowedSysctls is the allowlist of kernel tunables Spectra captures.
// Adding a key here is a deliberate decision — we don't dump all of
// sysctl -a because it's massive and noisy.
var AllowedSysctls = []string{
	"kern.maxfiles",
	"kern.maxproc",
	"kern.ipc.maxsockbuf",
	"kern.boottime",
	"vm.memory_pressure",
	"hw.ncpu",
	"hw.memsize",
}

// CollectSysctls returns the allowed sysctl key→value map. On Linux the
// values come from /proc/sys using the Linux allowlist; on macOS they come
// from sysctl(8). Keys absent on this kernel are omitted.
func CollectSysctls(run CmdRunner) map[string]string {
	if hostos.Current() == hostos.Linux {
		return collectSysctlsLinux("/proc/sys")
	}
	out := make(map[string]string, len(AllowedSysctls))
	for _, key := range AllowedSysctls {
		val, err := run("sysctl", "-n", key)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(val))
		if v != "" {
			out[key] = v
		}
	}
	return out
}
