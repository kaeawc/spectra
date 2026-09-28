package hostwatch

import (
	"context"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/proc"
	"golang.org/x/sys/unix"
)

func darwinLoad() ([3]float64, error) {
	var out [3]float64
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return out, err
	}
	if len(raw) < 16 {
		return out, fmt.Errorf("short vm.loadavg")
	}
	scale := float64(binary.NativeEndian.Uint32(raw[12:16]))
	if len(raw) >= 24 {
		scale = float64(binary.NativeEndian.Uint64(raw[16:24]))
	}
	if scale <= 0 {
		return out, fmt.Errorf("invalid vm.loadavg scale")
	}
	for i := range out {
		out[i] = float64(binary.NativeEndian.Uint32(raw[i*4:i*4+4])) / scale
	}
	return out, nil
}
func darwinMemory() (string, float64, float64, error) { return collectDarwinMemory() }
func darwinLimits(n int) map[string]LimitUsage {
	m := map[string]LimitUsage{}
	get := func(k string) int { v, _ := unix.SysctlUint32(k); return int(v) }
	m["files"] = usage(get("kern.num_files"), get("kern.maxfiles"))
	m["procs"] = usage(n, get("kern.maxproc"))
	m["procs_per_uid"] = usage(0, get("kern.maxprocperuid"))
	m["pty"] = usage(0, get("kern.tty.ptmx_max"))
	return m
}
func darwinThermal(ctx context.Context, run proc.Runner) (bool, error) {
	out, err := proc.Output(ctx, run, "pmset", "-g", "therm")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "CPU_Speed_Limit") {
			_, value, ok := strings.Cut(line, "=")
			if ok {
				n, e := strconv.Atoi(strings.TrimSpace(value))
				if e == nil {
					return n < 100, nil
				}
			}
		}
	}
	return false, nil
}
