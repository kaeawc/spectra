package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/pressure"
)

// pressureReport is the JSON shape of `spectra pressure`.
type pressureReport struct {
	PSI      *pressure.PSI      `json:"psi,omitempty"`
	Cgroup   *pressure.Cgroup   `json:"cgroup,omitempty"`
	Findings []pressure.Finding `json:"findings"`
	Notes    []string           `json:"notes,omitempty"`
}

func runPressure(args []string) int {
	return runPressureWithIO(args, os.Stdout, os.Stderr, "/proc", "/sys/fs/cgroup")
}

func runPressureWithIO(args []string, stdout, stderr io.Writer, procRoot, cgroupRoot string) int {
	fs := flag.NewFlagSet("spectra pressure", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	pid := fs.Int("pid", 0, "Also read the cgroup v2 accounting for this process")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if hostos.Resolve(hostos.Unknown) != hostos.Linux {
		fmt.Fprintf(stderr, "spectra pressure: %v (use `spectra memory` on macOS)\n", hostos.Unsupported("pressure-stall information"))
		return 1
	}
	rep := pressureReport{Findings: []pressure.Finding{}}
	psi, err := pressure.ReadPSI(procRoot)
	switch {
	case err == nil:
		rep.PSI = &psi
		rep.Findings = append(rep.Findings, pressure.AssessPSI(psi)...)
	case errors.Is(err, pressure.ErrUnavailable):
		rep.Notes = append(rep.Notes, "PSI is not available on this kernel (needs Linux 4.20+ with CONFIG_PSI, and not booted with psi=0)")
	default:
		fmt.Fprintf(stderr, "spectra pressure: %v\n", err)
		return 1
	}
	if *pid > 0 {
		cg, err := pressure.ReadCgroup(procRoot, cgroupRoot, *pid)
		switch {
		case err == nil:
			rep.Cgroup = &cg
			rep.Findings = append(rep.Findings, pressure.AssessCgroup(cg)...)
		case errors.Is(err, pressure.ErrUnavailable):
			rep.Notes = append(rep.Notes, fmt.Sprintf("pid %d is not on a cgroup v2 hierarchy; cgroup accounting skipped", *pid))
		default:
			fmt.Fprintf(stderr, "spectra pressure: %v\n", err)
			return 1
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printPressure(stdout, rep)
	}
	for _, f := range rep.Findings {
		if f.Severity != pressure.SeverityInfo {
			return 3
		}
	}
	return 0
}

func printPressure(w io.Writer, r pressureReport) {
	if r.PSI != nil {
		fmt.Fprintln(w, "=== pressure stall (share of the last 10s / 60s / 300s tasks were stalled) ===")
		for _, row := range []struct {
			name string
			res  pressure.Resource
		}{{"cpu", r.PSI.CPU}, {"memory", r.PSI.Memory}, {"io", r.PSI.IO}} {
			fmt.Fprintf(w, "  %-7s some %5.1f%% %5.1f%% %5.1f%%", row.name, row.res.Some.Avg10, row.res.Some.Avg60, row.res.Some.Avg300)
			if row.res.Full != nil && row.name != "cpu" {
				fmt.Fprintf(w, "   full %5.1f%% %5.1f%% %5.1f%%", row.res.Full.Avg10, row.res.Full.Avg60, row.res.Full.Avg300)
			}
			fmt.Fprintln(w)
		}
	}
	if c := r.Cgroup; c != nil {
		fmt.Fprintf(w, "=== cgroup %s ===\n", c.Path)
		if m := c.Memory; m != nil {
			limit := "unlimited"
			if m.EffectiveMax > 0 {
				limit = fmt.Sprintf("%d MiB", m.EffectiveMax>>20)
				if m.LimitedBy != "" && m.LimitedBy != c.Path {
					limit += " (from " + m.LimitedBy + ")"
				}
			}
			fmt.Fprintf(w, "  memory: %d MiB used (anon %d MiB, file %d MiB), limit %s, swap %d MiB\n", m.Current>>20, m.Anon>>20, m.File>>20, limit, m.SwapCurrent>>20)
			fmt.Fprintf(w, "  events: max=%d high=%d oom=%d oom_kill=%d\n", m.Events.Max, m.Events.High, m.Events.OOM, m.Events.OOMKill)
		}
		if cpu := c.CPU; cpu != nil {
			fmt.Fprintf(w, "  cpu:    %.1fs used, throttled %d/%d periods (%.1fs)\n", float64(cpu.UsageUsec)/1e6, cpu.NrThrottled, cpu.NrPeriods, float64(cpu.ThrottledUsec)/1e6)
		}
		if p := c.Pids; p != nil {
			fmt.Fprintf(w, "  pids:   %d / %d\n", p.Current, p.Max)
		}
	}
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "no pressure problems found")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(w, "[%s] %s: %s\n", f.Severity, f.Kind, f.Summary)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}
