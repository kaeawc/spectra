package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/helperclient"
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
	fetchDeep    func(durationMS int) ([]byte, error)
	clock        func() time.Time
	signalCh     func() (<-chan os.Signal, func())
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
		fetchDeep: func(durationMS int) ([]byte, error) {
			return helperclient.New().PowermetricsTasks(durationMS)
		},
		clock:    time.Now,
		signalCh: defaultPowerSignalCh,
	}
}

func defaultPowerSignalCh() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch); close(ch) }
}

// powerMode is the backend that produced a PowerReport. Modes are chosen at
// flag-parse time and stay fixed across a watch loop so consumers don't have
// to handle interleaved schemas.
type powerMode int

const (
	modeBaseline powerMode = iota // pmset + top -o power (no privilege)
	modeRusage                    // L1: proc_pid_rusage delta (no privilege)
	modeJoules                    // L1 + L2: + IOReport package joules (Apple Silicon, no privilege)
	modeDeep                      // L3: powermetrics tasks via helper (privileged)
)

func (m powerMode) String() string {
	switch m {
	case modeRusage:
		return "rusage"
	case modeJoules:
		return "joules"
	case modeDeep:
		return "deep"
	default:
		return "baseline"
	}
}

// PowerReport is the stable JSON envelope emitted by `spectra power --json`.
// Field names match across modes; backends fill what they can and missing
// data is omitted rather than zeroed.
type PowerReport struct {
	IntervalMS int64                    `json:"interval_ms"`
	Mode       string                   `json:"mode"`
	SampledAt  time.Time                `json:"sampled_at"`
	Battery    *BatteryInfo             `json:"battery,omitempty"`
	Thermal    *ThermalInfo             `json:"thermal,omitempty"`
	Assertions []sysinfo.PowerAssertion `json:"assertions,omitempty"`
	Package    *PackageEnergy           `json:"package,omitempty"`
	Processes  []ProcessEnergy          `json:"processes,omitempty"`
	Skipped    int                      `json:"skipped,omitempty"`
	Notes      []string                 `json:"notes,omitempty"`
}

type BatteryInfo struct {
	OnBattery bool `json:"on_battery"`
	Pct       int  `json:"pct,omitempty"`
}

type ThermalInfo struct {
	Pressure                string `json:"pressure,omitempty"`
	Throttled               bool   `json:"throttled,omitempty"`
	CPUSpeedLimitPct        int    `json:"cpu_speed_limit_pct,omitempty"`
	LowestCPUSpeedLimitPct  int    `json:"lowest_cpu_speed_limit_pct,omitempty"`
	AverageCPUSpeedLimitPct int    `json:"average_cpu_speed_limit_pct,omitempty"`
	PercentThermalThrottled int    `json:"percent_thermal_throttled,omitempty"`
}

type PackageEnergy struct {
	Joules     float64 `json:"joules,omitempty"`
	CPUJoules  float64 `json:"cpu_joules,omitempty"`
	GPUJoules  float64 `json:"gpu_joules,omitempty"`
	ANEJoules  float64 `json:"ane_joules,omitempty"`
	DRAMJoules float64 `json:"dram_joules,omitempty"`
	IntervalNS int64   `json:"interval_ns,omitempty"`
}

type ProcessWakeups struct {
	Interrupt uint64 `json:"interrupt,omitempty"`
	PkgIdle   uint64 `json:"pkg_idle,omitempty"`
}

type ProcessEnergy struct {
	PID            int                      `json:"pid"`
	Command        string                   `json:"command,omitempty"`
	BilledEnergyNJ uint64                   `json:"billed_energy_nj,omitempty"`
	GPUTimeNs      uint64                   `json:"gpu_time_ns,omitempty"`
	Wakeups        *ProcessWakeups          `json:"wakeups,omitempty"`
	Impact         *sysinfo.ImpactBreakdown `json:"impact,omitempty"`
	Source         string                   `json:"source"`
}

func runPowerWithIO(args []string, stdout, stderr io.Writer, deps powerDeps) int {
	fs := flag.NewFlagSet("spectra power", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printPowerUsage(stderr) }

	asJSON := fs.Bool("json", false, "Emit structured output (NDJSON when combined with --watch)")
	joules := fs.Bool("joules", false, "Sample SoC-wide energy via IOReport (Apple Silicon)")
	top := fs.Int("top", 0, "Rank top-N pids by ΔbilledEnergy over --interval (per-pid kernel-billed nanojoules via proc_pid_rusage)")
	interval := fs.Duration("interval", time.Second, "Sampling window for --joules / --top, and tick for --watch")
	deep := fs.Bool("deep", false, "Ground-truth per-pid energy via powermetrics (requires privileged helper)")
	deepDurationMS := fs.Int("deep-duration", 500, "Sample window in ms for --deep (min 100)")
	watch := fs.Bool("watch", false, "Re-run the chosen mode every --interval until SIGINT (NDJSON with --json)")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintln(stderr, "spectra power: --interval must be positive")
		return 2
	}

	mode := selectPowerMode(*top, *joules, *deep)
	cfg := powerRunCfg{
		mode:           mode,
		interval:       *interval,
		top:            *top,
		deepDurationMS: *deepDurationMS,
		asJSON:         *asJSON,
	}

	if *watch {
		return runPowerWatch(stdout, stderr, deps, cfg)
	}
	return runPowerOnce(stdout, stderr, deps, cfg)
}

func printPowerUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: spectra power [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Backends are layered. Bare `spectra power` is the pmset + top -o power baseline.")
	fmt.Fprintln(w, "Each flag below opts into a richer source; --deep implies --joules.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --top N           Top N pids by rusage energy delta              (no privilege)")
	fmt.Fprintln(w, "  --joules          Per-package joules via IOReport (Apple Silicon) (no privilege)")
	fmt.Fprintln(w, "  --deep            Powermetrics ground truth                       (requires helper)")
	fmt.Fprintln(w, "  --deep-duration   Sample window in ms for --deep (default 500)")
	fmt.Fprintln(w, "  --watch           Re-run every --interval; emits NDJSON with --json (inherits)")
	fmt.Fprintln(w, "  --json            Structured output (stable schema across modes)   (inherits)")
	fmt.Fprintln(w, "  --interval D      Sample window or watch tick (default 1s)        (inherits)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  spectra power")
	fmt.Fprintln(w, "  spectra power --top 10 --json")
	fmt.Fprintln(w, "  spectra power --joules --watch --interval 2s")
	fmt.Fprintln(w, "  spectra power --deep --json | jq .package.joules")
}

func selectPowerMode(top int, joules, deep bool) powerMode {
	switch {
	case deep:
		return modeDeep
	case joules:
		return modeJoules
	case top > 0:
		return modeRusage
	default:
		return modeBaseline
	}
}

type powerRunCfg struct {
	mode           powerMode
	interval       time.Duration
	top            int
	deepDurationMS int
	asJSON         bool
}

// runPowerOnce runs the chosen mode a single time. Human output preserves
// the existing per-mode tables (baseline / SoC / energy top / deep) so users
// who pipe `spectra power` into other tools keep working. JSON output goes
// through the unified PowerReport envelope so consumers can parse one shape.
func runPowerOnce(stdout, stderr io.Writer, deps powerDeps, cfg powerRunCfg) int {
	report, exit, ok := collectPower(stdout, stderr, deps, cfg)
	if !ok {
		return exit
	}
	if cfg.asJSON {
		writePowerJSON(stdout, report, false)
		return 0
	}
	printPowerHuman(stdout, deps, cfg, report)
	return 0
}

// runPowerWatch loops until SIGINT. JSON mode emits compact NDJSON
// (one object per line, parseable by `jq -c`); human mode clears the screen
// with VT100 codes (no curses, so `tee` still works) and prints a final
// summary line on exit.
func runPowerWatch(stdout, stderr io.Writer, deps powerDeps, cfg powerRunCfg) int {
	sig, stop := deps.signalCh()
	defer stop()

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()

	emit := func() int {
		report, exit, ok := collectPower(stdout, stderr, deps, cfg)
		if !ok {
			return exit
		}
		if cfg.asJSON {
			writePowerJSON(stdout, report, true)
		} else {
			fmt.Fprint(stdout, "\x1b[H\x1b[2J")
			fmt.Fprintf(stdout, "=== Power state (%s, watch %s) ===\n", cfg.mode, cfg.interval)
			printPowerHuman(stdout, deps, cfg, report)
		}
		return 0
	}

	if rc := emit(); rc != 0 {
		return rc
	}

	for {
		select {
		case _, ok := <-sig:
			if !ok {
				return 0
			}
			if cfg.asJSON {
				enc := json.NewEncoder(stdout)
				_ = enc.Encode(map[string]any{"watch_stopped": true})
			} else {
				fmt.Fprintln(stdout, "--- watch stopped ---")
			}
			return 0
		case <-ticker.C:
			if rc := emit(); rc != 0 {
				return rc
			}
		}
	}
}

// collectPower runs the appropriate L1/L2/L3 sources for the requested mode
// and assembles a PowerReport. Returns (report, exit_code, ok). If ok is
// false the caller should return exit_code immediately (e.g. unsupported
// hardware on --joules, --top on non-darwin).
func collectPower(stdout, stderr io.Writer, deps powerDeps, cfg powerRunCfg) (PowerReport, int, bool) {
	state := deps.collect()
	report := PowerReport{
		IntervalMS: cfg.interval.Milliseconds(),
		Mode:       cfg.mode.String(),
		SampledAt:  deps.clock(),
	}
	fillPowerStateSections(&report, state)

	switch cfg.mode {
	case modeBaseline:
		report.Processes = processesFromEnergyTopUsers(state.EnergyTopUsers)
		return report, 0, true
	case modeRusage:
		deltas, exit, ok := collectRusageDeltas(stderr, deps, cfg.interval)
		if !ok {
			return report, exit, false
		}
		report.Processes = processesFromRusage(deltas, state.Assertions, cfg.top)
		report.Skipped = countSkipped(deps, deltas)
		return report, 0, true
	case modeJoules:
		soc, exit, ok := collectSoC(stderr, deps, cfg.interval)
		if !ok {
			return report, exit, false
		}
		report.Package = packageFromSoC(*soc)
		return report, 0, true
	case modeDeep:
		soc, _, _ := collectSoC(stderr, deps, cfg.interval) // joules implied; omit silently if unavailable
		if soc != nil {
			report.Package = packageFromSoC(*soc)
		}
		tasks, note := collectDeepTasks(stderr, deps.fetchDeep, normalizeDeepDuration(cfg.deepDurationMS))
		if note != "" {
			report.Notes = append(report.Notes, note)
		}
		if tasks != nil {
			report.Processes = processesFromTasks(*tasks)
		}
		return report, 0, true
	}
	return report, 0, true
}

func fillPowerStateSections(r *PowerReport, s sysinfo.PowerState) {
	if s.OnBattery || s.BatteryPct > 0 {
		r.Battery = &BatteryInfo{OnBattery: s.OnBattery, Pct: s.BatteryPct}
	}
	if s.ThermalPressure != "" || s.ThermalThrottled || s.CPUSpeedLimitPct > 0 {
		r.Thermal = &ThermalInfo{
			Pressure:                s.ThermalPressure,
			Throttled:               s.ThermalThrottled,
			CPUSpeedLimitPct:        s.CPUSpeedLimitPct,
			LowestCPUSpeedLimitPct:  s.LowestCPUSpeedLimitPct,
			AverageCPUSpeedLimitPct: s.AverageCPUSpeedLimitPct,
			PercentThermalThrottled: s.PercentThermalThrottled,
		}
	}
	if len(s.Assertions) > 0 {
		r.Assertions = s.Assertions
	}
}

func processesFromEnergyTopUsers(users []sysinfo.EnergyUser) []ProcessEnergy {
	if len(users) == 0 {
		return nil
	}
	rows := make([]ProcessEnergy, 0, len(users))
	for _, u := range users {
		rows = append(rows, ProcessEnergy{
			PID:     u.PID,
			Command: u.Command,
			Impact:  &sysinfo.ImpactBreakdown{Total: u.EnergyImpact, FromEnergy: u.EnergyImpact},
			Source:  "top",
		})
	}
	return rows
}

func processesFromRusage(deltas []sysinfo.EnergyDelta, assertions []sysinfo.PowerAssertion, top int) []ProcessEnergy {
	if len(deltas) == 0 {
		return nil
	}
	ranked := sysinfo.RankedEnergy(deltas, top)
	weights, _ := sysinfo.LoadWeights(os.Getenv)
	rows := make([]ProcessEnergy, 0, len(ranked))
	for _, d := range ranked {
		impact := sysinfo.ComputeImpact(sysinfo.FromRusage(d), assertions, weights)
		row := ProcessEnergy{
			PID:            d.PID,
			Command:        d.Command,
			BilledEnergyNJ: d.BilledEnergyNJ,
			Source:         "rusage",
		}
		if d.InterruptWakeups > 0 || d.PkgIdleWakeups > 0 {
			row.Wakeups = &ProcessWakeups{Interrupt: d.InterruptWakeups, PkgIdle: d.PkgIdleWakeups}
		}
		impactCopy := impact
		row.Impact = &impactCopy
		rows = append(rows, row)
	}
	return rows
}

func processesFromTasks(p sysinfo.PowermetricsTasks) []ProcessEnergy {
	if len(p.Tasks) == 0 {
		return nil
	}
	sorted := make([]sysinfo.TaskPowerSample, len(p.Tasks))
	copy(sorted, p.Tasks)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].EnergyImpact > sorted[j].EnergyImpact })
	rows := make([]ProcessEnergy, 0, len(sorted))
	for _, t := range sorted {
		rows = append(rows, ProcessEnergy{
			PID:       t.PID,
			Command:   t.Command,
			GPUTimeNs: t.GPUNs,
			Impact:    &sysinfo.ImpactBreakdown{Total: t.EnergyImpact, FromEnergy: t.EnergyImpact},
			Source:    "powermetrics",
		})
	}
	return rows
}

func packageFromSoC(p sysinfo.SoCPower) *PackageEnergy {
	return &PackageEnergy{
		Joules:     p.PackageJoules,
		CPUJoules:  p.CPUPJoules + p.CPUEJoules,
		GPUJoules:  p.GPUJoules,
		ANEJoules:  p.ANEJoules,
		DRAMJoules: p.DRAMJoules,
		IntervalNS: p.Interval.Nanoseconds(),
	}
}

func collectRusageDeltas(stderr io.Writer, deps powerDeps, interval time.Duration) ([]sysinfo.EnergyDelta, int, bool) {
	ctx := context.Background()
	procs := deps.procs(ctx)
	if len(procs) == 0 {
		fmt.Fprintln(stderr, "no processes to sample")
		return nil, 1, false
	}
	pids := make([]int, 0, len(procs))
	for _, p := range procs {
		if p.PID > 0 {
			pids = append(pids, p.PID)
		}
	}
	sort.Ints(pids)
	deltas, err := deps.sampleRusage(ctx, interval, pids)
	if err != nil {
		if errors.Is(err, sysinfo.ErrRusageUnsupported) {
			fmt.Fprintln(stderr, "per-pid energy unavailable: built without cgo or non-darwin")
			return nil, 3, false
		}
		fmt.Fprintln(stderr, "sample failed:", err)
		return nil, 1, false
	}
	cmds := make(map[int]string, len(procs))
	for _, p := range procs {
		cmds[p.PID] = p.Command
	}
	for i := range deltas {
		if deltas[i].Command == "" {
			deltas[i].Command = cmds[deltas[i].PID]
		}
	}
	return deltas, 0, true
}

func countSkipped(deps powerDeps, deltas []sysinfo.EnergyDelta) int {
	ctx := context.Background()
	procs := deps.procs(ctx)
	pids := 0
	for _, p := range procs {
		if p.PID > 0 {
			pids++
		}
	}
	return pids - len(deltas)
}

func collectSoC(stderr io.Writer, deps powerDeps, interval time.Duration) (*sysinfo.SoCPower, int, bool) {
	p, err := deps.sample(interval)
	if err != nil {
		if errors.Is(err, sysinfo.ErrUnsupportedHardware) {
			fmt.Fprintln(stderr, "SoC power sampling is unavailable on this hardware (requires Apple Silicon on macOS 12+).")
			return nil, 3, false
		}
		fmt.Fprintf(stderr, "sample failed: %v\n", err)
		return nil, 1, false
	}
	return &p, 0, true
}

func normalizeDeepDuration(ms int) int {
	if ms < 100 {
		return 100
	}
	return ms
}

func collectDeepTasks(stderr io.Writer, fetch func(int) ([]byte, error), durationMS int) (*sysinfo.PowermetricsTasks, string) {
	plist, err := fetch(durationMS)
	switch {
	case helperclient.IsUnavailable(err):
		fmt.Fprintln(stderr, "spectra power: privileged helper not installed.")
		fmt.Fprintln(stderr, "Install with: sudo spectra install-helper")
		fmt.Fprintln(stderr, "Falling back to L1+L2 (no-privilege) energy estimate.")
		return nil, "helper unavailable; degraded to L1+L2"
	case err != nil:
		fmt.Fprintf(stderr, "spectra power: --deep failed: %v\n", err)
		fmt.Fprintln(stderr, "Falling back to L1+L2 (no-privilege) energy estimate.")
		return nil, fmt.Sprintf("deep failed: %v", err)
	}
	parsed, perr := sysinfo.ParseTasksPlist(plist)
	if perr != nil {
		fmt.Fprintf(stderr, "spectra power: parsing powermetrics plist: %v\n", perr)
		return nil, fmt.Sprintf("plist parse error: %v", perr)
	}
	return &parsed, ""
}

func writePowerJSON(w io.Writer, r PowerReport, ndjson bool) {
	enc := json.NewEncoder(w)
	if !ndjson {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(r)
}

// printPowerHuman keeps the per-mode human tables intact so existing pipes
// keep working. Bare `spectra power` stays the legacy pmset+top view.
func printPowerHuman(stdout io.Writer, deps powerDeps, cfg powerRunCfg, r PowerReport) {
	state := powerStateFromReport(r, deps.collect)
	switch cfg.mode {
	case modeBaseline:
		printPowerState(stdout, state)
	case modeRusage:
		legacy := energyTopFromReport(r, cfg)
		printEnergyTop(stdout, legacy, cfg.interval)
	case modeJoules:
		if r.Package != nil {
			printSoCPower(stdout, socFromPackage(*r.Package))
		}
	case modeDeep:
		printPowerState(stdout, state)
		if r.Package != nil {
			fmt.Fprintln(stdout)
			printSoCPower(stdout, socFromPackage(*r.Package))
		}
		if len(r.Processes) > 0 {
			printDeepProcesses(stdout, r.Processes, 10)
		}
	}
}

// powerStateFromReport reconstructs the legacy PowerState shape from the
// envelope (battery + thermal + assertions + EnergyTopUsers) so the existing
// printPowerState renderer keeps working without re-collecting.
func powerStateFromReport(r PowerReport, fallback func() sysinfo.PowerState) sysinfo.PowerState {
	s := sysinfo.PowerState{Assertions: r.Assertions}
	if r.Battery != nil {
		s.OnBattery = r.Battery.OnBattery
		s.BatteryPct = r.Battery.Pct
	}
	if r.Thermal != nil {
		s.ThermalPressure = r.Thermal.Pressure
		s.ThermalThrottled = r.Thermal.Throttled
		s.CPUSpeedLimitPct = r.Thermal.CPUSpeedLimitPct
		s.LowestCPUSpeedLimitPct = r.Thermal.LowestCPUSpeedLimitPct
		s.AverageCPUSpeedLimitPct = r.Thermal.AverageCPUSpeedLimitPct
		s.PercentThermalThrottled = r.Thermal.PercentThermalThrottled
	}
	// The legacy CPUSpeedLimitSamples view drives the "% of samples" string;
	// the envelope doesn't carry the per-sample series, so pull it from the
	// fallback PowerState when needed.
	if r.Thermal != nil && r.Thermal.Throttled {
		s.CPUSpeedLimitSamples = fallback().CPUSpeedLimitSamples
	}
	for _, p := range r.Processes {
		impact := 0.0
		if p.Impact != nil {
			impact = p.Impact.Total
		}
		s.EnergyTopUsers = append(s.EnergyTopUsers, sysinfo.EnergyUser{PID: p.PID, EnergyImpact: impact, Command: p.Command})
	}
	return s
}

func energyTopFromReport(r PowerReport, cfg powerRunCfg) energyTopLegacy {
	top := make([]sysinfo.EnergyDelta, 0, len(r.Processes))
	for _, p := range r.Processes {
		d := sysinfo.EnergyDelta{
			PID:            p.PID,
			Command:        p.Command,
			Interval:       cfg.interval,
			BilledEnergyNJ: p.BilledEnergyNJ,
		}
		if p.Wakeups != nil {
			d.InterruptWakeups = p.Wakeups.Interrupt
			d.PkgIdleWakeups = p.Wakeups.PkgIdle
		}
		top = append(top, d)
	}
	return energyTopLegacy{intervalNS: cfg.interval.Nanoseconds(), skipped: r.Skipped, top: top}
}

type energyTopLegacy struct {
	intervalNS int64
	skipped    int
	top        []sysinfo.EnergyDelta
}

func socFromPackage(p PackageEnergy) sysinfo.SoCPower {
	return sysinfo.SoCPower{
		Interval:      time.Duration(p.IntervalNS),
		CPUPJoules:    p.CPUJoules,
		GPUJoules:     p.GPUJoules,
		ANEJoules:     p.ANEJoules,
		DRAMJoules:    p.DRAMJoules,
		PackageJoules: p.Joules,
	}
}

func printEnergyTop(w io.Writer, r energyTopLegacy, interval time.Duration) {
	fmt.Fprintf(w, "=== Energy top users (Δ over %s) ===\n", interval)
	if r.skipped > 0 {
		fmt.Fprintf(w, "skipped: %d pid(s) (EPERM or vanished)\n", r.skipped)
	}
	fmt.Fprintf(w, "%-7s  %-13s  %-9s  %-8s  %s\n",
		"PID", "BILLED(nJ)", "WAKEUPS", "CPU(ms)", "COMMAND")
	fmt.Fprintln(w, strings.Repeat("-", 64))
	for _, d := range r.top {
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

func printDeepProcesses(w io.Writer, procs []ProcessEnergy, topN int) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "=== Deep (powermetrics --samplers tasks) ===")
	if len(procs) == 0 {
		fmt.Fprintln(w, "no per-pid task samples returned")
		return
	}
	fmt.Fprintf(w, "  %-7s  %-8s  %-10s  %s\n", "PID", "IMPACT", "GPU(ms)", "COMMAND")
	fmt.Fprintln(w, "  "+strings.Repeat("-", 56))
	if topN > 0 && topN < len(procs) {
		procs = procs[:topN]
	}
	for _, p := range procs {
		impact := 0.0
		if p.Impact != nil {
			impact = p.Impact.Total
		}
		fmt.Fprintf(w, "  %-7d  %-8.1f  %-10.2f  %s\n",
			p.PID, impact, float64(p.GPUTimeNs)/1e6, p.Command)
	}
}
