package hostwatch

import (
	"strconv"
	"strings"
)

func linuxLoad(read func(string) ([]byte, error)) ([3]float64, error) {
	var v [3]float64
	b, err := read("/proc/loadavg")
	if err != nil {
		return v, err
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return v, strconv.ErrSyntax
	}
	for i := range v {
		v[i], err = strconv.ParseFloat(f[i], 64)
		if err != nil {
			return v, err
		}
	}
	return v, nil
}
func linuxMemory(read func(string) ([]byte, error)) (string, float64, float64, error) {
	b, err := read("/proc/meminfo")
	if err != nil {
		return "unknown", 0, 0, err
	}
	m := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			v, _ := strconv.ParseFloat(f[1], 64)
			m[strings.TrimSuffix(f[0], ":")] = v
		}
	}
	if m["MemTotal"] <= 0 {
		return "unknown", 0, 0, strconv.ErrSyntax
	}
	free := 100 * m["MemAvailable"] / m["MemTotal"]
	p := "normal"
	if free < 5 {
		p = "critical"
	} else if free < 15 {
		p = "warn"
	}
	return p, (m["SwapTotal"] - m["SwapFree"]) / 1024, free, nil
}
func linuxLimits(read func(string) ([]byte, error), n int) map[string]LimitUsage {
	get := func(path string) int {
		b, err := read(path)
		if err != nil {
			return 0
		}
		f := strings.Fields(string(b))
		if len(f) == 0 {
			return 0
		}
		v, _ := strconv.Atoi(f[0])
		return v
	}
	m := map[string]LimitUsage{}
	m["pty"] = usage(get("/proc/sys/kernel/pty/nr"), get("/proc/sys/kernel/pty/max"))
	m["files"] = usage(get("/proc/sys/fs/file-nr"), get("/proc/sys/fs/file-max"))
	m["procs"] = usage(n, get("/proc/sys/kernel/pid_max"))
	m["procs_per_uid"] = usage(0, 0)
	return m
}
