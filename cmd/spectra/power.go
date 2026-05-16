package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func runPower(args []string) int {
	return runPowerWithIO(args, os.Stdout, os.Stderr, defaultPowerDeps())
}

type powerDeps struct {
	collect func() sysinfo.PowerState
	sample  func(time.Duration) (sysinfo.SoCPower, error)
}

func defaultPowerDeps() powerDeps {
	return powerDeps{
		collect: func() sysinfo.PowerState {
			return sysinfo.CollectPower(sysinfo.DefaultRunner)
		},
		sample: sysinfo.SampleSoCPower,
	}
}

func runPowerWithIO(args []string, stdout, stderr io.Writer, deps powerDeps) int {
	fs := flag.NewFlagSet("spectra power", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	joules := fs.Bool("joules", false, "Sample SoC-wide energy via IOReport (Apple Silicon)")
	interval := fs.Duration("interval", time.Second, "Sampling window for --joules")
	if err := fs.Parse(args); err != nil {
		return 2
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
