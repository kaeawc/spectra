// Package sysload reads the whole-machine memory-pressure and swap signals
// macOS exposes without root. It complements the per-process and thermal
// collectors so a single "why is this Mac slow?" triage can rank causes.
package sysload

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Runner runs a command and returns combined stdout. Injected for testability.
type Runner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Load is the machine's current memory-pressure and swap state.
type Load struct {
	MemoryPressure string  `json:"memory_pressure"` // normal, warn, critical, unknown
	PressureLevel  int     `json:"pressure_level"`
	SwapUsedMB     float64 `json:"swap_used_mb"`
	SwapTotalMB    float64 `json:"swap_total_mb"`
}

var (
	swapUsedRe  = regexp.MustCompile(`used\s*=\s*([0-9.]+)([MG]?)`)
	swapTotalRe = regexp.MustCompile(`total\s*=\s*([0-9.]+)([MG]?)`)
)

// Collect reads memory pressure and swap usage.
func Collect(run Runner) Load {
	l := Load{MemoryPressure: "unknown"}
	if out, err := run("sysctl", "-n", "kern.memorystatus_vm_pressure_level"); err == nil {
		if lvl, aerr := strconv.Atoi(strings.TrimSpace(string(out))); aerr == nil {
			l.PressureLevel = lvl
			l.MemoryPressure = pressureLabel(lvl)
		}
	}
	if out, err := run("sysctl", "-n", "vm.swapusage"); err == nil {
		l.SwapUsedMB = parseSwapField(swapUsedRe, string(out))
		l.SwapTotalMB = parseSwapField(swapTotalRe, string(out))
	}
	return l
}

func pressureLabel(lvl int) string {
	switch lvl {
	case 1:
		return "normal"
	case 2:
		return "warn"
	case 4:
		return "critical"
	default:
		return fmt.Sprintf("level-%d", lvl)
	}
}

// parseSwapField pulls a megabyte value out of a `vm.swapusage` string like
// "total = 2048.00M  used = 669.88M  free = 1378.12M".
func parseSwapField(re *regexp.Regexp, s string) float64 {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	if m[2] == "G" {
		v *= 1024
	}
	return v
}
