package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

func runPower(args []string) int {
	return runPowerWithIO(args, os.Stdout, os.Stderr, defaultPowerDeps())
}

type powerDeps struct {
	collect      func() sysinfo.PowerState
	sample       func(time.Duration) (sysinfo.SoCPower, error)
	procs        func(context.Context) []process.Info
	sampleRusage func(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error)
	clock        func() time.Time
}

func defaultPowerDeps() powerDeps {
	return powerDeps{
		collect: func() sysinfo.PowerState {
			return sysinfo.CollectPower(sysinfo.DefaultRunner)
		},
		sample: sysinfo.SampleSoCPower,
		procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{})
		},
		sampleRusage: func(ctx context.Context, interval time.Duration, pids []int) ([]sysinfo.EnergyDelta, error) {
			return sysinfo.EnergySampler{Interval: interval}.Sample(ctx, pids)
		},
		clock: time.Now,
	}
}

func runPowerWithIO(args []string, stdout, stderr io.Writer, deps powerDeps) int {
	fs := flag.NewFlagSet("spectra power", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	joules := fs.Bool("joules", false, "Sample SoC-wide energy via IOReport (Apple Silicon)")
	top := fs.Int("top", 0, "Rank top-N pids by ΔbilledEnergy over --interval (per-pid kernel-billed nanojoules via proc_pid_rusage)")
	interval := fs.Duration("interval", time.Second, "Sampling window for --joules / --top")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *top > 0 {
		return runRusageTop(stdout, stderr, *interval, *top, *asJSON, deps)
	}
	if *joules {
		return runJoulesSample(stdout, stderr, *interval, *asJSON, deps.sample)
	}

	state := deps.collect()

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printPowerState(stdout, state)
	return 0
}

func runJoulesSample(stdout, stderr io.Writer, interval time.Duration, asJSON bool, sample func(time.Duration) (sysinfo.SoCPower, error)) int {
	p, err := sample(interval)
	if err != nil {
		if errors.Is(err, sysinfo.ErrUnsupportedHardware) {
			fmt.Fprintln(stderr, "SoC power sampling is unavailable on this hardware (requires Apple Silicon on macOS 12+).")
			return 3
		}
		fmt.Fprintf(stderr, "sample failed: %v\n", err)
		return 1
	}
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(p)
		return 0
	}
	printSoCPower(stdout, p)
	return 0
}

type EnergyTopReport struct {
	IntervalNS int64                 `json:"interval_ns"`
	SampledAt  time.Time             `json:"sampled_at"`
	Skipped    int                   `json:"skipped"`
	Top        []sysinfo.EnergyDelta `json:"top"`
}

func runRusageTop(stdout, stderr io.Writer, interval time.Duration, top int, asJSON bool, deps powerDeps) int {
	ctx := context.Background()
	procs := deps.procs(ctx)
	if len(procs) == 0 {
		fmt.Fprintln(stderr, "no processes to sample")
		return 1
	}
	pids := make([]int, 0, len(procs))
	cmds := make(map[int]string, len(procs))
	for _, p := range procs {
		if p.PID > 0 {
			pids = append(pids, p.PID)
			cmds[p.PID] = p.Command
		}
	}
	sort.Ints(pids)

	deltas, err := deps.sampleRusage(ctx, interval, pids)
	if err != nil {
		if errors.Is(err, sysinfo.ErrRusageUnsupported) {
			fmt.Fprintln(stderr, "per-pid energy unavailable: built without cgo or non-darwin")
			return 3
		}
		fmt.Fprintln(stderr, "sample failed:", err)
		return 1
	}
	ranked := sysinfo.RankedEnergy(deltas, top)
	for i := range ranked {
		ranked[i].Command = cmds[ranked[i].PID]
	}

	clock := deps.clock
	if clock == nil {
		clock = time.Now
	}
	report := EnergyTopReport{
		IntervalNS: interval.Nanoseconds(),
		SampledAt:  clock(),
		Skipped:    len(pids) - len(deltas),
		Top:        ranked,
	}

	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printEnergyTop(stdout, report, interval)
	return 0
}

func printEnergyTop(w io.Writer, r EnergyTopReport, interval time.Duration) {
	fmt.Fprintf(w, "=== Energy top users (Δ over %s) ===\n", interval)
	if r.Skipped > 0 {
		fmt.Fprintf(w, "skipped: %d pid(s) (EPERM or vanished)\n", r.Skipped)
	}
	fmt.Fprintf(w, "%-7s  %-13s  %-9s  %-8s  %s\n",
		"PID", "BILLED(nJ)", "WAKEUPS", "CPU(ms)", "COMMAND")
	fmt.Fprintln(w, strings.Repeat("-", 64))
	for _, d := range r.Top {
		cpuMs := (d.UserNs + d.SystemNs) / 1_000_000
		fmt.Fprintf(w, "%-7d  %-13d  %-9d  %-8d  %s\n",
			d.PID, d.BilledEnergyNJ, d.InterruptWakeups, cpuMs, d.Command)
	}
}

func printSoCPower(w io.Writer, p sysinfo.SoCPower) {
	fmt.Fprintln(w, "=== SoC power (IOReport) ===")
	fmt.Fprintf(w, "Package:   %.2f J over %s  (%.2f W)\n", p.PackageJoules, p.Interval, p.Watts())
	fmt.Fprintf(w, "  CPU P:   %.2f J\n", p.CPUPJoules)
	fmt.Fprintf(w, "  CPU E:   %.2f J\n", p.CPUEJoules)
	fmt.Fprintf(w, "  GPU:     %.2f J\n", p.GPUJoules)
	fmt.Fprintf(w, "  ANE:     %.2f J\n", p.ANEJoules)
	fmt.Fprintf(w, "  DRAM:    %.2f J\n", p.DRAMJoules)
}

func printPowerState(w io.Writer, s sysinfo.PowerState) {
	fmt.Fprintln(w, "=== Power state ===")

	if s.OnBattery {
		fmt.Fprintf(w, "source:    battery (%d%%)\n", s.BatteryPct)
	} else {
		fmt.Fprintf(w, "source:    AC power\n")
		if s.BatteryPct > 0 {
			fmt.Fprintf(w, "battery:   %d%% (charging)\n", s.BatteryPct)
		}
	}

	if s.ThermalPressure != "" {
		fmt.Fprintf(w, "thermal:   %s\n", s.ThermalPressure)
	}
	if s.CPUSpeedLimitPct > 0 {
		fmt.Fprintf(w, "cpu limit: %d%%\n", s.CPUSpeedLimitPct)
	}
	if s.ThermalThrottled {
		fmt.Fprintf(w, "throttled: yes")
		if len(s.CPUSpeedLimitSamples) > 1 {
			fmt.Fprintf(w, " (lowest %d%%, average %d%%, %d%% of samples)",
				s.LowestCPUSpeedLimitPct,
				s.AverageCPUSpeedLimitPct,
				s.PercentThermalThrottled,
			)
		}
		fmt.Fprintln(w)
	}

	if len(s.Assertions) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "assertions (%d):\n", len(s.Assertions))
		for _, a := range s.Assertions {
			name := ""
			if a.Name != "" {
				name = fmt.Sprintf(" %q", a.Name)
			}
			fmt.Fprintf(w, "  pid %-7d  %-30s%s\n", a.PID, a.Type, name)
		}
	}

	if len(s.EnergyTopUsers) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "energy top users:\n")
		fmt.Fprintf(w, "  %-7s  %-8s  %s\n", "PID", "IMPACT", "COMMAND")
		fmt.Fprintln(w, "  "+strings.Repeat("-", 40))
		for _, u := range s.EnergyTopUsers {
			fmt.Fprintf(w, "  %-7d  %-8.1f  %s\n", u.PID, u.EnergyImpact, u.Command)
		}
	}
}
