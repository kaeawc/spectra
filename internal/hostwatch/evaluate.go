package hostwatch

import (
	"fmt"
	"math"
	"sort"
	"time"
)

type Condition struct {
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

func Evaluate(cfg Config, window []Sample) []Condition {
	if len(window) == 0 {
		return nil
	}
	s := window[len(window)-1]
	var out []Condition
	out = append(out, evalLoad(cfg, window, s)...)
	out = append(out, evalMemory(cfg, window, s)...)
	out = append(out, evalLimits(cfg, s)...)
	out = append(out, evalDiskThermal(cfg, window, s)...)
	out = append(out, evalKinds(cfg, window, s)...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func evalLoad(c Config, w []Sample, s Sample) []Condition {
	if s.NCPU <= 0 {
		return nil
	}
	n := float64(s.NCPU)
	var out []Condition
	if sustained(w, c.Load.Sustain, func(x Sample) bool { return x.Load1 > c.Load.CriticalMultiple*n }) {
		out = append(out, Condition{"load.high", "critical", "Host load is critical", fmt.Sprintf("1-minute load %.1f across %d CPUs", s.Load1, s.NCPU)})
	} else if sustained(w, c.Load.Sustain, func(x Sample) bool { return x.Load1 > c.Load.WarnMultiple*n }) {
		out = append(out, Condition{"load.high", "warn", "Host load is high", fmt.Sprintf("1-minute load %.1f across %d CPUs", s.Load1, s.NCPU)})
	}
	if s.Load1 > n && slope(w, time.Duration(c.Load.TrendMinutes)*time.Minute) > c.Load.TrendSlopeMultiple*n {
		out = append(out, Condition{"load.climbing", "warn", "Load climbing", fmt.Sprintf("1-minute load %.1f is rising", s.Load1)})
	}
	return out
}
func evalMemory(c Config, w []Sample, s Sample) []Condition {
	var out []Condition
	if s.MemoryPressure == "critical" {
		out = append(out, Condition{"memory.pressure", "critical", "Memory pressure is critical", fmt.Sprintf("swap %.0f MB", s.SwapUsedMB)})
	} else if sustained(w, c.Memory.WarnSustain, func(x Sample) bool { return x.MemoryPressure == "warn" }) {
		out = append(out, Condition{"memory.pressure", "warn", "Memory pressure is elevated", fmt.Sprintf("swap %.0f MB", s.SwapUsedMB)})
	}
	for i := len(w) - 2; i >= 0; i-- {
		x := w[i]
		if s.At.Sub(x.At) > 5*time.Minute {
			break
		}
		if s.SwapUsedMB-x.SwapUsedMB > c.Memory.SwapGrowthMB {
			out = append(out, Condition{"memory.swap_growth", "warn", "Swap growing quickly", fmt.Sprintf("swap grew %.0f MB", s.SwapUsedMB-x.SwapUsedMB)})
			break
		}
	}
	return out
}
func evalLimits(c Config, s Sample) []Condition {
	var out []Condition
	for key, v := range s.Limits {
		if key == "procs" || key == "procs_per_uid" {
			continue
		}
		if v.Limit <= 0 {
			continue
		}
		if v.Pct > c.Limits.CriticalPct {
			out = append(out, Condition{"limit:" + key, "critical", "Resource limit near exhaustion", fmt.Sprintf("%s: %.0f%%", key, v.Pct)})
		} else if v.Pct > c.Limits.WarnPct {
			out = append(out, Condition{"limit:" + key, "warn", "Resource limit elevated", fmt.Sprintf("%s: %.0f%%", key, v.Pct)})
		}
	}
	return out
}
func evalDiskThermal(c Config, w []Sample, s Sample) []Condition {
	var out []Condition
	if s.DataFreeGB >= 0 && s.DataFreeGB < c.Disk.CriticalGB {
		out = append(out, Condition{"disk.free", "critical", "Data disk nearly full", fmt.Sprintf("%.1f GB free", s.DataFreeGB)})
	} else if s.DataFreeGB >= 0 && s.DataFreeGB < c.Disk.WarnGB {
		out = append(out, Condition{"disk.free", "warn", "Data disk low", fmt.Sprintf("%.1f GB free", s.DataFreeGB)})
	}
	if sustained(w, c.Thermal.Sustain, func(x Sample) bool { return x.ThermalThrottled }) {
		out = append(out, Condition{"thermal.throttled", "warn", "CPU thermally throttled", "thermal speed limit active"})
	}
	return out
}
func evalKinds(c Config, w []Sample, s Sample) []Condition {
	var out []Condition
	for kind, stat := range s.Kinds {
		if kind != "other" && stat.CPUPct >= c.Kinds.CPUPct && sustained(w, c.Kinds.CPUSustain, func(x Sample) bool { return x.Kinds[kind].CPUPct >= c.Kinds.CPUPct }) {
			out = append(out, Condition{"kind.cpu:" + kind, "warn", kind + " CPU high", fmt.Sprintf("%.0f%% CPU", stat.CPUPct)})
		}
	}
	for _, v := range []struct {
		key              string
		count, threshold int
	}{{"gradle", s.Kinds["gradle"].Count, c.Kinds.Gradle}, {"simulator", s.Kinds["simulator"].Count, c.Kinds.Simulator}, {"qemu", s.Kinds["qemu"].Count, c.Kinds.QEMU}, {"agents", s.Kinds["codex"].Count + s.Kinds["claude"].Count, c.Kinds.Agents}} {
		if v.count >= v.threshold && sustained(w, c.Kinds.CountSustain, func(x Sample) bool { return kindCount(x, v.key) >= v.threshold }) {
			out = append(out, Condition{"kind.count:" + v.key, "warn", v.key + " process count high", fmt.Sprintf("%d processes", v.count)})
		}
	}
	return out
}
func kindCount(s Sample, kind string) int {
	if kind == "agents" {
		return s.Kinds["codex"].Count + s.Kinds["claude"].Count
	}
	return s.Kinds[kind].Count
}
func sustained(w []Sample, n int, ok func(Sample) bool) bool {
	if n < 1 {
		n = 1
	}
	if len(w) < n {
		return false
	}
	for _, s := range w[len(w)-n:] {
		if !ok(s) {
			return false
		}
	}
	return true
}
func slope(w []Sample, d time.Duration) float64 {
	if len(w) < 2 {
		return 0
	}
	end := w[len(w)-1].At
	var n, sx, sy, sxx, sxy float64
	for _, v := range w {
		if end.Sub(v.At) > d {
			continue
		}
		x := v.At.Sub(end).Minutes()
		n++
		sx += x
		sy += v.Load1
		sxx += x * x
		sxy += x * v.Load1
	}
	den := n*sxx - sx*sx
	if n < 2 || math.Abs(den) < 1e-9 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
