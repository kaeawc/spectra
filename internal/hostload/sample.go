package hostload

import (
	"context"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
)

// cpuTimes is one process's cumulative CPU and start time, both in clock ticks.
type cpuTimes struct{ cpu, start int64 }

// Sampler measures recent CPU on Linux, where the collector only supplies a
// whole-lifetime average. macOS ps already reports a decaying recent average,
// so Collect returns its result untouched there.
type Sampler struct {
	Host    Host
	Collect func(ctx context.Context) []process.Info
	Now     func() time.Time
	Sleep   func(ctx context.Context, d time.Duration)
}

// Sample collects processes and, on Linux with a positive interval, replaces
// each surviving process's CPUPct with its CPU use over the window between two
// /proc readings. Processes are matched by pid and start time so a reused pid
// is not diffed; processes seen in only one reading keep the collector value
// (new) or are dropped (gone). The returned window is zero when lifetime
// averages were used.
func (s Sampler) Sample(ctx context.Context, interval time.Duration) ([]process.Info, time.Duration) {
	if interval <= 0 || s.Sleep == nil || s.Now == nil || s.Host.os() != hostos.Linux {
		return s.Collect(ctx), 0
	}
	before := s.readTimes(s.Collect(ctx))
	t0 := s.Now()
	s.Sleep(ctx, interval)
	procs := s.Collect(ctx)
	after := s.readTimes(procs)
	window := s.Now().Sub(t0)
	if window <= 0 {
		return procs, 0
	}
	diffed := 0
	for i := range procs {
		a, okA := before[procs[i].PID]
		b, okB := after[procs[i].PID]
		if !okA || !okB || a.start != b.start || b.cpu < a.cpu {
			continue
		}
		secs := float64(b.cpu-a.cpu) / float64(process.LinuxClkTck)
		procs[i].CPUPct = 100 * secs / window.Seconds()
		procs[i].CPUPercent = 0
		diffed++
	}
	if diffed == 0 {
		return procs, 0
	}
	return procs, window
}

func (s Sampler) readTimes(procs []process.Info) map[int]cpuTimes {
	out := make(map[int]cpuTimes, len(procs))
	for _, p := range procs {
		data, err := s.Host.readFile("/proc/" + strconv.Itoa(p.PID) + "/stat")
		if err != nil {
			continue
		}
		if cpu, start, ok := process.ParseLinuxCPUTimes(data); ok {
			out[p.PID] = cpuTimes{cpu: cpu, start: start}
		}
	}
	return out
}
