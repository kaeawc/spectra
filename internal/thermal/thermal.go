// Package thermal reports whether the host is thermally throttled: macOS via
// `pmset -g therm`, Linux via the /sys thermal, cpufreq and thermal_throttle
// interfaces. Every filesystem read goes through an injected fs.FS.
package thermal

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// Verdict is the tri-state thermal outcome.
type Verdict string

const (
	Nominal   Verdict = "nominal"
	Warm      Verdict = "warm"
	Throttled Verdict = "throttled"
)

// Exit codes of `spectra thermal`, keyed by verdict.
const (
	ExitNominal   = 0
	ExitWarm      = 3
	ExitThrottled = 4
)

const (
	// WarmMarginC: a zone this close below a passive/hot/critical trip is about
	// to throttle, so it is worth surfacing before the CPU actually slows.
	WarmMarginC = 5.0
	// FreqCapRatio: a scaling_max_freq below this share of cpuinfo_max_freq is a
	// cap; it only counts as throttling when a thermal zone is also warm, since
	// no_turbo and power profiles cap a cold machine too.
	FreqCapRatio = 0.90
	// MaxPlausibleTripC: trips above this (and at or below 0) are firmware
	// placeholders, not real thresholds.
	MaxPlausibleTripC = 150.0
	// FullSpeedPct is the pmset limit value meaning "not limited".
	FullSpeedPct = 100
)

// DefaultWatchInterval is the sampling period for --watch.
const DefaultWatchInterval = 2 * time.Second

// Finding is one piece of evidence and the verdict it supports.
type Finding struct {
	Verdict  Verdict `json:"verdict"`
	Evidence string  `json:"evidence"`
}

// Limits are the fields `pmset -g therm` printed. A nil field was not printed
// and is unknown; it is never assumed to be 100 (Apple silicon often prints
// only the warning lines).
type Limits struct {
	SchedulerLimit     *int `json:"cpu_scheduler_limit,omitempty"`
	AvailableCPUs      *int `json:"cpu_available_cpus,omitempty"`
	SpeedLimit         *int `json:"cpu_speed_limit,omitempty"`
	ThermalWarning     *int `json:"thermal_warning_level,omitempty"`
	PerformanceWarning *int `json:"performance_warning_level,omitempty"`
}

// Zone is one /sys/class/thermal/thermal_zone*.
type Zone struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	TempC float64 `json:"temp_c"`
	Trips []Trip  `json:"trips,omitempty"`
}

// Trip is one trip point of a zone.
type Trip struct {
	Type  string  `json:"type"`
	TempC float64 `json:"temp_c"`
}

// Cooling is one /sys/class/thermal/cooling_device*.
type Cooling struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	CurState int64  `json:"cur_state"`
	MaxState int64  `json:"max_state"`
}

// FreqCap is a CPU whose scaling_max_freq sits below its hardware maximum.
type FreqCap struct {
	CPU        string `json:"cpu"`
	ScalingKHz int64  `json:"scaling_max_khz"`
	MaxKHz     int64  `json:"cpuinfo_max_khz"`
}

// Report is the result of one Collect.
type Report struct {
	OS       string           `json:"os"`
	Verdict  Verdict          `json:"verdict"`
	Evidence string           `json:"evidence"`
	Findings []Finding        `json:"findings"`
	Limits   *Limits          `json:"limits,omitempty"`
	Zones    []Zone           `json:"zones,omitempty"`
	Cooling  []Cooling        `json:"cooling_devices,omitempty"`
	FreqCaps []FreqCap        `json:"freq_caps,omitempty"`
	Counters map[string]int64 `json:"throttle_counts,omitempty"`
	Notes    []string         `json:"notes,omitempty"`
}

// CounterIncrease is a thermal_throttle counter that grew during a watch.
type CounterIncrease struct {
	Path string `json:"path"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// WatchReport is the outcome of Watch: the worst sample, with any throttle
// counter increase folded into the verdict.
type WatchReport struct {
	Report
	Samples          int               `json:"samples"`
	CounterIncreases []CounterIncrease `json:"counter_increases,omitempty"`
}

// Collector gathers thermal state. Zero-value fields resolve to the host.
type Collector struct {
	OS     hostos.Kind
	Runner proc.Runner
	FS     fs.FS
	Clock  clock.Clock
	// Sleep waits for d or until ctx ends; nil uses a real timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (c *Collector) fsys() fs.FS {
	if c.FS == nil {
		return os.DirFS("/")
	}
	return c.FS
}

func (c *Collector) clk() clock.Clock {
	if c.Clock == nil {
		return clock.Default
	}
	return c.Clock
}

func (c *Collector) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Collect takes one reading.
func (c *Collector) Collect(ctx context.Context) (Report, error) {
	switch k := hostos.Resolve(c.OS); k {
	case hostos.Darwin:
		return c.collectDarwin(ctx)
	case hostos.Linux:
		return c.collectLinux(), nil
	default:
		return Report{}, hostos.Unsupported("thermal inspection")
	}
}

func severity(v Verdict) int {
	switch v {
	case Throttled:
		return 2
	case Warm:
		return 1
	}
	return 0
}

// decide sets Verdict and Evidence from the first finding of the worst severity.
func (r *Report) decide(nominalEvidence string) {
	r.Verdict, r.Evidence = Nominal, nominalEvidence
	best := 0
	for _, f := range r.Findings {
		if s := severity(f.Verdict); s > best {
			best = s
			r.Verdict, r.Evidence = f.Verdict, f.Evidence
		}
	}
}

// Watch samples every interval until duration has elapsed on the injected
// clock, returning the worst verdict seen and any throttle-counter increase.
func (c *Collector) Watch(ctx context.Context, duration, interval time.Duration) (WatchReport, error) {
	if interval <= 0 {
		return WatchReport{}, fmt.Errorf("watch interval must be positive, got %s", interval)
	}
	first, err := c.Collect(ctx)
	if err != nil {
		return WatchReport{}, err
	}
	worst := first
	tracker := newCounterTracker(first.Counters)
	samples := 1
	start := c.clk().Now()
	for {
		remaining := duration - c.clk().Now().Sub(start)
		if remaining <= 0 {
			break
		}
		// Cap the sleep so the final sample lands inside the requested window.
		if err := c.sleep(ctx, min(interval, remaining)); err != nil {
			if ctx.Err() != nil {
				break
			}
			return WatchReport{}, fmt.Errorf("watch sleep: %w", err)
		}
		cur, err := c.Collect(ctx)
		if err != nil {
			return WatchReport{}, err
		}
		samples++
		tracker.observe(cur.Counters)
		if severity(cur.Verdict) > severity(worst.Verdict) {
			worst = cur
		}
	}
	out := WatchReport{Report: worst, Samples: samples}
	out.CounterIncreases = tracker.increases()
	if len(out.CounterIncreases) > 0 {
		inc := out.CounterIncreases[0]
		ev := fmt.Sprintf("%s rose %d -> %d during watch", inc.Path, inc.From, inc.To)
		out.Findings = append([]Finding{{Verdict: Throttled, Evidence: ev}}, out.Findings...)
		out.Verdict, out.Evidence = Throttled, ev
	}
	return out, nil
}
