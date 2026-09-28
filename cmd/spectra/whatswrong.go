package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
	"github.com/kaeawc/spectra/internal/watchclient"
)

type slowSignals struct {
	Load  sysload.Load
	Power sysinfo.PowerState
	Procs []process.Info
}

type slowCause struct {
	Severity string `json:"severity"` // high, medium, low
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

type slowDeps struct {
	load    func() sysload.Load
	power   func() sysinfo.PowerState
	procs   func() []process.Info
	connect func(context.Context) (slowWatchClient, bool)
	now     func() time.Time
}

type slowWatchClient interface {
	Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error)
	Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error)
	Close() error
}

func defaultSlowDeps() slowDeps {
	return slowDeps{
		load:  func() sysload.Load { return sysload.Collect(sysload.DefaultRunner) },
		power: func() sysinfo.PowerState { return sysinfo.CollectPower(sysinfo.DefaultRunner) },
		procs: func() []process.Info { return process.CollectAll(context.Background(), process.CollectOptions{}) },
		connect: func(ctx context.Context) (slowWatchClient, bool) {
			paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
			if err != nil {
				return nil, false
			}
			return watchclient.Connect(ctx, os.Getenv, paths)
		},
		now: time.Now,
	}
}

func runWhatswrong(args []string) int {
	return runWhatswrongWithIO(args, os.Stdout, os.Stderr, defaultSlowDeps())
}

func runWhatswrongWithIO(args []string, stdout, stderr io.Writer, deps slowDeps) int {
	fs := flag.NewFlagSet("whatswrong", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	noDaemon := fs.Bool("no-daemon", false, "disable daemon enrichment")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	sig := slowSignals{Load: deps.load(), Power: deps.power(), Procs: deps.procs()}
	causes := rankSlowCauses(sig)
	var daemonAlerts []hostwatch.Alert
	var trend string
	if !*noDaemon && deps.connect != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if client, ok := deps.connect(ctx); ok {
			func() {
				defer client.Close()
				_, alerts, err := client.Current(ctx)
				if err != nil {
					cliLogger.Debug("whatswrong daemon current unavailable", "error", err)
					return
				}
				clockNow := deps.now
				if clockNow == nil {
					clockNow = time.Now
				}
				now := clockNow()
				samples, err := client.Samples(ctx, now.Add(-5*time.Minute), 240)
				if err != nil {
					cliLogger.Debug("whatswrong daemon samples unavailable", "error", err)
					return
				}
				daemonAlerts = alerts
				trend = summarizeTrend(samples)
			}()
		} else {
			cliLogger.Debug("whatswrong daemon unavailable")
		}
		cancel()
	}
	causes = mergeDaemonCauses(causes, daemonAlerts)
	if *asJSON {
		out := struct {
			Causes       []slowCause       `json:"causes"`
			DaemonAlerts []hostwatch.Alert `json:"daemon_alerts,omitempty"`
			Trend        string            `json:"trend,omitempty"`
			Signals      struct {
				Load   sysload.Load       `json:"load"`
				Power  sysinfo.PowerState `json:"power"`
				TopCPU *process.Info      `json:"top_cpu,omitempty"`
				TopRSS *process.Info      `json:"top_rss,omitempty"`
			} `json:"signals"`
		}{Causes: causes, DaemonAlerts: daemonAlerts, Trend: trend}
		out.Signals.Load = sig.Load
		out.Signals.Power = sig.Power
		if p, ok := topByCPU(sig.Procs); ok {
			out.Signals.TopCPU = &p
		}
		if p, ok := topByRSS(sig.Procs); ok {
			out.Signals.TopRSS = &p
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderWhatswrong(stdout, causes)
	if trend != "" {
		fmt.Fprintln(stdout, "Trend:", trend)
	}
	return 0
}

func mergeDaemonCauses(causes []slowCause, alerts []hostwatch.Alert) []slowCause {
	seen := make(map[string]bool, len(causes))
	for _, c := range causes {
		seen[c.Title] = true
	}
	for _, a := range alerts {
		if seen[a.Title] {
			continue
		}
		severity := "medium"
		if a.Severity == "critical" {
			severity = "high"
		}
		causes = append(causes, slowCause{Severity: severity, Title: a.Title, Detail: a.Detail})
		seen[a.Title] = true
	}
	sort.SliceStable(causes, func(i, j int) bool { return severityRank(causes[i].Severity) < severityRank(causes[j].Severity) })
	return causes
}

func summarizeTrend(samples []hostwatch.Sample) string {
	if len(samples) == 0 {
		return ""
	}
	latest := samples[0]
	for _, s := range samples {
		if s.At.After(latest.At) {
			latest = s
		}
	}
	elevated := false
	for _, s := range samples {
		if s.MemoryPressure == "warn" || s.MemoryPressure == "critical" {
			elevated = true
			break
		}
	}
	pressure := "normal"
	if elevated {
		pressure = "elevated in the last 5 min"
	}
	return fmt.Sprintf("load %.2f/%.2f/%.2f; memory pressure %s", latest.Load1, latest.Load5, latest.Load15, pressure)
}

func rankSlowCauses(s slowSignals) []slowCause {
	var causes []slowCause
	causes = appendMemoryCauses(causes, s.Load)
	causes = appendThermalCauses(causes, s.Power)
	causes = appendProcessCauses(causes, s.Procs)
	sort.SliceStable(causes, func(i, j int) bool {
		return severityRank(causes[i].Severity) < severityRank(causes[j].Severity)
	})
	return causes
}

func appendMemoryCauses(causes []slowCause, l sysload.Load) []slowCause {
	switch l.MemoryPressure {
	case "critical":
		causes = append(causes, slowCause{"high", "Memory pressure is critical",
			fmt.Sprintf("swap in use: %.0f MB of %.0f MB — the machine is out of RAM headroom", l.SwapUsedMB, l.SwapTotalMB)})
	case "warn":
		causes = append(causes, slowCause{"medium", "Memory pressure is elevated",
			fmt.Sprintf("swap in use: %.0f MB", l.SwapUsedMB)})
	default:
		if l.SwapUsedMB >= 2048 {
			causes = append(causes, slowCause{"medium", "Heavy swap usage",
				fmt.Sprintf("%.0f MB swapped — the machine is over-committed on RAM", l.SwapUsedMB)})
		}
	}
	return causes
}

func appendThermalCauses(causes []slowCause, p sysinfo.PowerState) []slowCause {
	if p.ThermalThrottled {
		return append(causes, slowCause{"high", "CPU is thermally throttled",
			fmt.Sprintf("%d%% of recent samples were speed-limited (thermal state: %s)", p.PercentThermalThrottled, p.ThermalPressure)})
	}
	if p.ThermalPressure == "serious" || p.ThermalPressure == "critical" {
		return append(causes, slowCause{"medium", "Thermal pressure is " + p.ThermalPressure,
			"the system may begin throttling"})
	}
	return causes
}

func appendProcessCauses(causes []slowCause, procs []process.Info) []slowCause {
	if p, ok := topByCPU(procs); ok && p.CPUPct >= 80 {
		causes = append(causes, slowCause{"medium", "A process is using heavy CPU",
			fmt.Sprintf("%s (pid %d) at %.0f%% CPU", p.Command, p.PID, p.CPUPct)})
	}
	if p, ok := topByRSS(procs); ok && p.RSSKiB >= 4*1024*1024 {
		causes = append(causes, slowCause{"low", "A process is holding a lot of memory",
			fmt.Sprintf("%s (pid %d) at %.1f GB RSS", p.Command, p.PID, float64(p.RSSKiB)/1024/1024)})
	}
	return causes
}

func topByCPU(procs []process.Info) (process.Info, bool) {
	var top process.Info
	found := false
	for _, p := range procs {
		if !found || p.CPUPct > top.CPUPct {
			top, found = p, true
		}
	}
	return top, found
}

func topByRSS(procs []process.Info) (process.Info, bool) {
	var top process.Info
	found := false
	for _, p := range procs {
		if !found || p.RSSKiB > top.RSSKiB {
			top, found = p, true
		}
	}
	return top, found
}

func severityRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}

func renderWhatswrong(w io.Writer, causes []slowCause) {
	if len(causes) == 0 {
		fmt.Fprintln(w, "Nothing obviously wrong: memory, thermal, CPU, and swap all look nominal.")
		return
	}
	fmt.Fprintln(w, "Why this Mac may be slow right now:")
	for i, c := range causes {
		fmt.Fprintf(w, "%d) [%s] %s\n", i+1, c.Severity, c.Title)
		if c.Detail != "" {
			fmt.Fprintf(w, "     %s\n", c.Detail)
		}
	}
}
