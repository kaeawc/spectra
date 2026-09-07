package sysinfo

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LinuxAllowedSysctls is the Linux counterpart to AllowedSysctls: a curated
// set of tunables read from /proc/sys. Keys use the conventional
// dotted-path form; the path is derived by replacing '.' with '/'.
var LinuxAllowedSysctls = []string{
	"fs.file-max",
	"fs.nr_open",
	"kernel.pid_max",
	"kernel.threads-max",
	"vm.swappiness",
	"vm.max_map_count",
	"net.core.somaxconn",
}

// collectSysctlsLinux reads the Linux allowlist from a /proc/sys tree
// rooted at procSysRoot (normally "/proc/sys"). Missing keys are omitted.
func collectSysctlsLinux(procSysRoot string) map[string]string {
	out := make(map[string]string, len(LinuxAllowedSysctls))
	for _, key := range LinuxAllowedSysctls {
		rel := strings.ReplaceAll(key, ".", string(os.PathSeparator))
		data, err := os.ReadFile(filepath.Join(procSysRoot, rel))
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(data)); v != "" {
			out[key] = v
		}
	}
	return out
}

// collectPowerLinux reads battery and AC state from a power-supply tree
// rooted at root (normally "/sys/class/power_supply"). Thermal pressure has
// no standard Linux equivalent, so those fields are left zero. Best-effort:
// a missing or unreadable tree yields a zero PowerState.
func collectPowerLinux(root string) PowerState {
	var ps PowerState
	entries, err := os.ReadDir(root)
	if err != nil {
		return ps
	}
	acOnline := false
	acSeen := false
	batterySeen := false
	discharging := false

	readField := func(supply, field string) string {
		b, err := os.ReadFile(filepath.Join(root, supply, field))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(b))
	}

	for _, e := range entries {
		name := e.Name()
		switch readField(name, "type") {
		case "Mains", "USB", "UPS":
			acSeen = true
			if readField(name, "online") == "1" {
				acOnline = true
			}
		case "Battery":
			batterySeen = true
			if pct, err := strconv.Atoi(readField(name, "capacity")); err == nil {
				ps.BatteryPct = pct
			}
			if strings.EqualFold(readField(name, "status"), "Discharging") {
				discharging = true
			}
		}
	}

	// On battery when discharging, or when an AC supply exists but none is
	// online. A machine with no battery at all stays OnBattery=false.
	ps.OnBattery = discharging || (batterySeen && acSeen && !acOnline)
	return ps
}
