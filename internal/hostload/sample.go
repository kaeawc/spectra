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

// CPUBasis says what a process's CPUPct means.
type CPUBasis string

const (
	// BasisWindow is a Linux delta over a measured window.
	BasisWindow CPUBasis = "window"
	// BasisRecent is macOS ps's decaying recent average.
	BasisRecent CPUBasis = "recent"
	// BasisLifetime is a Linux whole-lifetime average: sampling was off or
	// nothing could be diffed.
	BasisLifetime CPUBasis = "lifetime"
)

// startTolerance absorbs the whole-second precision of process start times.
const startTolerance = time.Second

// Sample collects processes and, on Linux with a positive interval, replaces
// each surviving process's CPUPct with its CPU use over the window between two
// /proc readings. Processes are matched by pid and start time so a reused pid
// is not diffed; processes that exited are dropped. A process that started
// inside the window is scored on all its CPU over the window; one that was
// merely unreadable in the first reading keeps the collector value. The
// returned window is zero unless the basis is BasisWindow.
func (s Sampler) Sample(ctx context.Context, interval time.Duration) ([]process.Info, time.Duration, CPUBasis) {
	if s.Host.os() != hostos.Linux {
		return s.Collect(ctx), 0, BasisRecent
	}
	if interval <= 0 || s.Sleep == nil || s.Now == nil {
		return s.Collect(ctx), 0, BasisLifetime
	}
	before := s.readTimes(s.Collect(ctx))
	t0 := s.Now()
	s.Sleep(ctx, interval)
	procs := s.Collect(ctx)
	after := s.readTimes(procs)
	window := s.Now().Sub(t0)
	if window <= 0 {
		return procs, 0, BasisLifetime
	}
	diffed := 0
	for i := range procs {
		if applyWindow(&procs[i], before, after, t0, window) {
			diffed++
		}
	}
	if diffed == 0 {
		return procs, 0, BasisLifetime
	}
	return procs, window, BasisWindow
}

// applyWindow rewrites p's CPU% from the two readings and reports whether it did.
func applyWindow(p *process.Info, before, after map[int]cpuTimes, t0 time.Time, window time.Duration) bool {
	b, okB := after[p.PID]
	if !okB {
		return false
	}
	a, okA := before[p.PID]
	var cpuTicks int64
	switch {
	case okA && a.start == b.start && b.cpu >= a.cpu:
		cpuTicks = b.cpu - a.cpu
	case !okA && !p.StartTime.IsZero() && !p.StartTime.Before(t0.Add(-startTolerance)):
		// Started inside the window, so all of its CPU was accrued in it. The
		// tolerance covers start times having whole-second precision.
		cpuTicks = b.cpu
	default:
		return false
	}
	p.CPUPct = 100 * (float64(cpuTicks) / float64(process.LinuxClkTck)) / window.Seconds()
	p.CPUPercent = 0
	return true
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
