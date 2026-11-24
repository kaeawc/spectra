package idle

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// HostSampler measures per-process CPU over a window on the real host. Linux
// reuses hostload's two /proc/<pid>/stat readings; macOS diffs the cumulative
// CPU time `ps -o time` reports, since ps's own %cpu is a decaying average.
type HostSampler struct {
	OS       hostos.Kind
	Runner   proc.Runner
	ReadFile func(string) ([]byte, error)
	Collect  func(ctx context.Context) []process.Info
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration)
	// Baseline is passed to hostload's Linux sampler.
	Baseline func(window time.Duration) (hostload.Baseline, bool)
}

// Sample collects the process table and CPU over window.
func (h HostSampler) Sample(ctx context.Context, window time.Duration) (Sample, error) {
	if window <= 0 {
		return Sample{}, errors.New("sample window must be positive")
	}
	var s Sample
	var err error
	switch hostos.Resolve(h.OS) {
	case hostos.Linux:
		s = h.sampleLinux(ctx, window)
	case hostos.Darwin:
		s, err = h.sampleDarwin(ctx, window)
	default:
		return Sample{}, hostos.Unsupported("idle sampling")
	}
	if err != nil {
		return Sample{}, err
	}
	if err := ctx.Err(); err != nil {
		return Sample{}, fmt.Errorf("sampling CPU: %w", err)
	}
	return s, nil
}

func (h HostSampler) sampleLinux(ctx context.Context, window time.Duration) Sample {
	ls := hostload.Sampler{
		Host:    hostload.Host{OS: hostos.Linux, Runner: h.Runner, ReadFile: h.ReadFile},
		Collect: h.Collect, Now: h.Now, Sleep: h.Sleep, Baseline: h.Baseline,
	}
	procs, win, basis := ls.Sample(ctx, window)
	s := Sample{Procs: procs, Window: win, Basis: string(basis), CPU: map[int]float64{}}
	if basis == hostload.BasisWindow {
		for _, p := range procs {
			s.CPU[p.PID] = p.CPUPct
		}
	}
	return s
}

func (h HostSampler) sampleDarwin(ctx context.Context, window time.Duration) (Sample, error) {
	before, err := h.psTimes(ctx)
	if err != nil {
		return Sample{}, err
	}
	t0 := h.Now()
	h.Sleep(ctx, window)
	procs := h.Collect(ctx)
	after, err := h.psTimes(ctx)
	if err != nil {
		return Sample{}, err
	}
	win := h.Now().Sub(t0)
	s := Sample{Procs: procs, Window: win, Basis: BasisWindow, CPU: map[int]float64{}}
	if win <= 0 {
		s.Basis, s.Window = "none", 0
		return s, nil
	}
	newSince := t0.Add(-startTolerance)
	for _, p := range procs {
		b, okB := after[p.PID]
		if !okB {
			continue
		}
		a, okA := before[p.PID]
		started := !p.StartTime.IsZero() && !p.StartTime.Before(newSince)
		var used time.Duration
		switch {
		case started:
			// New, or a reused pid: all its CPU accrued inside the window.
			used = b
		case okA && b >= a:
			used = b - a
		default:
			continue
		}
		s.CPU[p.PID] = 100 * used.Seconds() / win.Seconds()
	}
	return s, nil
}

func (h HostSampler) psTimes(ctx context.Context) (map[int]time.Duration, error) {
	run := h.Runner
	if run == nil {
		run = proc.Default
	}
	out, err := proc.Output(ctx, run, "ps", "-axo", "pid=,time=")
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return ParsePsTimes(out), nil
}

// ParsePsTimes parses `ps -o pid=,time=` rows into cumulative CPU time.
func ParsePsTimes(out []byte) map[int]time.Duration {
	res := map[int]time.Duration{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		if d, ok := ParseCPUTime(f[1]); ok {
			res[pid] = d
		}
	}
	return res
}

// ParseCPUTime parses ps's [[dd-]hh:]mm:ss[.cc] cumulative CPU time.
func ParseCPUTime(s string) (time.Duration, bool) {
	var days int64
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.ParseInt(d, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	sec, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil || sec < 0 {
		return 0, false
	}
	total := float64(days*86400) + sec
	mult := 60.0
	for i := len(parts) - 2; i >= 0; i-- {
		n, err := strconv.ParseInt(parts[i], 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		total += float64(n) * mult
		mult *= 60
	}
	return time.Duration(total * float64(time.Second)), true
}
