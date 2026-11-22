// Package whatswrong assembles the ranked "why is this Mac slow right now?"
// report that `spectra whatswrong --json` prints and the daemon's
// whatswrong.current method serves, so both emit the same JSON.
package whatswrong

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
)

// DaemonTimeout bounds the daemon alert and trend enrichment.
const DaemonTimeout = time.Second

// Signals are the raw inputs the ranking reads.
type Signals struct {
	Load  sysload.Load
	Power sysinfo.PowerState
	Procs []process.Info
	Now   time.Time // optional; used for family ages
	Cores int       // logical CPU count; 0 if unknown
}

// Cause is one ranked finding.
type Cause struct {
	Severity string `json:"severity"` // high, medium, low
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

// WatchClient is the slice of the daemon's host-watch API the enrichment uses.
type WatchClient interface {
	Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error)
	Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error)
	Close() error
}

// Deps are the collection seams. Load, Power, and Procs are required; a nil
// Connect disables daemon enrichment, a nil XPC disables XPC attribution.
type Deps struct {
	Load    func(context.Context) sysload.Load
	Power   func(context.Context) sysinfo.PowerState
	Procs   func(context.Context) []process.Info
	Connect func(context.Context) (WatchClient, bool)
	Now     func() time.Time
	CPUs    func() int // logical CPU count; nil or 0 means unknown
	// SelfPID names the collecting process. Its direct children (the
	// concurrent top, pmset, and sysctl probes) are dropped from Procs so
	// they never rank as causes. Nil keeps every process.
	SelfPID func() int
	XPC     XPCAttributeFunc
	Logger  logger.Logger
}

// DefaultDeps returns production collectors whose children die with ctx.
// Connect is left nil; callers choose how to reach host-watch data.
func DefaultDeps() Deps {
	return Deps{
		Load:  func(ctx context.Context) sysload.Load { return sysload.Collect(proc.BoundedRunner(ctx)) },
		Power: func(ctx context.Context) sysinfo.PowerState { return sysinfo.CollectPower(proc.BoundedRunner(ctx)) },
		Procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{})
		},
		Now:     time.Now,
		CPUs:    runtime.NumCPU,
		SelfPID: os.Getpid,
		XPC:     DefaultXPCAttribute,
	}
}

// ReportSignals are the raw signals echoed so the ranking is auditable.
type ReportSignals struct {
	Load   sysload.Load       `json:"load"`
	Power  sysinfo.PowerState `json:"power"`
	TopCPU *process.Info      `json:"top_cpu,omitempty"`
	TopRSS *process.Info      `json:"top_rss,omitempty"`
}

// Report is the `spectra whatswrong --json` object.
type Report struct {
	Causes       []Cause           `json:"causes"`
	Families     []process.Family  `json:"families,omitempty"`
	DaemonAlerts []hostwatch.Alert `json:"daemon_alerts,omitempty"`
	Trend        string            `json:"trend,omitempty"`
	Signals      ReportSignals     `json:"signals"`
}

// Collect gathers the signals and assembles the report. The load, power, and
// process probes run concurrently; daemon enrichment is bounded by
// DaemonTimeout and XPC attribution by its own timeout.
func Collect(ctx context.Context, deps Deps) Report {
	log := deps.Logger
	if log == nil {
		log = logger.Discard()
	}
	sig := collectSignals(ctx, log, deps)
	causes := RankCauses(sig)
	causes = appendXPCCauses(ctx, log, causes, sig.Procs, deps.XPC)
	alerts, trend := enrich(ctx, log, deps)
	r := Report{
		Causes:       mergeDaemonCauses(causes, alerts),
		Families:     reportableFamilies(sig),
		DaemonAlerts: alerts,
		Trend:        trend,
	}
	r.Signals.Load = sig.Load
	r.Signals.Power = sig.Power
	if p, ok := topByCPU(sig.Procs); ok {
		r.Signals.TopCPU = &p
	}
	if p, ok := topByRSS(sig.Procs); ok {
		r.Signals.TopRSS = &p
	}
	return r
}

// collectSignals runs the probes concurrently. A probe that panics leaves its
// zero value (memory pressure "unknown"), as a failed probe does, and is
// logged rather than taking down the caller.
func collectSignals(ctx context.Context, log logger.Logger, deps Deps) Signals {
	sig := Signals{Load: sysload.Load{MemoryPressure: "unknown"}}
	var wg sync.WaitGroup
	probe := func(name string, f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Debug("whatswrong probe panicked", "probe", name, "panic", r)
				}
			}()
			f()
		}()
	}
	probe("load", func() { sig.Load = deps.Load(ctx) })
	probe("power", func() { sig.Power = deps.Power(ctx) })
	probe("procs", func() { sig.Procs = withoutChildrenOf(deps.Procs(ctx), deps.SelfPID) })
	wg.Wait()
	if deps.CPUs != nil {
		sig.Cores = deps.CPUs()
	}
	if deps.Now != nil {
		sig.Now = deps.Now()
	}
	return sig
}

// withoutChildrenOf drops the direct children of self(), the collector's own
// probe subprocesses that the process listing can catch mid-run.
func withoutChildrenOf(procs []process.Info, self func() int) []process.Info {
	if self == nil {
		return procs
	}
	pid := self()
	if pid <= 0 {
		return procs
	}
	var out []process.Info
	for _, p := range procs {
		if p.PPID != pid {
			out = append(out, p)
		}
	}
	return out
}

// enrich reads the daemon's active alerts and recent trend; any failure or
// timeout leaves both empty.
func enrich(ctx context.Context, log logger.Logger, deps Deps) ([]hostwatch.Alert, string) {
	if deps.Connect == nil {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(ctx, DaemonTimeout)
	defer cancel()
	client, ok := deps.Connect(ctx)
	if !ok {
		log.Debug("whatswrong daemon unavailable")
		return nil, ""
	}
	defer client.Close()
	_, alerts, err := client.Current(ctx)
	if err != nil {
		log.Debug("whatswrong daemon current unavailable", "error", err)
		return nil, ""
	}
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	samples, err := client.Samples(ctx, now().Add(-5*time.Minute), 240)
	if err != nil {
		log.Debug("whatswrong daemon samples unavailable", "error", err)
		return nil, ""
	}
	return alerts, summarizeTrend(samples)
}

// LocalWatchSource is the in-process host-watch service the daemon owns.
type LocalWatchSource interface {
	Current() (hostwatch.Sample, []hostwatch.Alert)
	Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error)
}

// LocalWatch adapts an in-process host-watch service to Deps.Connect, so the
// daemon enriches its own report without dialing its own socket.
func LocalWatch(src LocalWatchSource) func(context.Context) (WatchClient, bool) {
	return func(context.Context) (WatchClient, bool) { return localWatch{src}, true }
}

type localWatch struct{ src LocalWatchSource }

func (l localWatch) Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error) {
	s, a := l.src.Current()
	return s, a, nil
}

func (l localWatch) Samples(ctx context.Context, since time.Time, limit int) ([]hostwatch.Sample, error) {
	return l.src.Samples(ctx, since, limit)
}

func (localWatch) Close() error { return nil }

func mergeDaemonCauses(causes []Cause, alerts []hostwatch.Alert) []Cause {
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
		causes = append(causes, Cause{Severity: severity, Title: a.Title, Detail: a.Detail})
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

// RankCauses orders findings from the signals, most severe first.
func RankCauses(s Signals) []Cause {
	var causes []Cause
	causes = appendMemoryCauses(causes, s.Load)
	causes = appendThermalCauses(causes, s.Power)
	causes = appendFamilyCauses(causes, slowFamilies(s.Procs, s.Now), s.Cores)
	causes = appendProcessCauses(causes, s.Procs)
	sort.SliceStable(causes, func(i, j int) bool {
		return severityRank(causes[i].Severity) < severityRank(causes[j].Severity)
	})
	return causes
}

func appendMemoryCauses(causes []Cause, l sysload.Load) []Cause {
	switch l.MemoryPressure {
	case "critical":
		causes = append(causes, Cause{"high", "Memory pressure is critical",
			fmt.Sprintf("swap in use: %.0f MB of %.0f MB — the machine is out of RAM headroom", l.SwapUsedMB, l.SwapTotalMB)})
	case "warn":
		causes = append(causes, Cause{"medium", "Memory pressure is elevated",
			fmt.Sprintf("swap in use: %.0f MB", l.SwapUsedMB)})
	default:
		if l.SwapUsedMB >= 2048 {
			causes = append(causes, Cause{"medium", "Heavy swap usage",
				fmt.Sprintf("%.0f MB swapped — the machine is over-committed on RAM", l.SwapUsedMB)})
		}
	}
	return causes
}

func appendThermalCauses(causes []Cause, p sysinfo.PowerState) []Cause {
	if p.ThermalThrottled {
		return append(causes, Cause{"high", "CPU is thermally throttled",
			fmt.Sprintf("%d%% of recent samples were speed-limited (thermal state: %s)", p.PercentThermalThrottled, p.ThermalPressure)})
	}
	if p.ThermalPressure == "serious" || p.ThermalPressure == "critical" {
		return append(causes, Cause{"medium", "Thermal pressure is " + p.ThermalPressure,
			"the system may begin throttling"})
	}
	return causes
}

func appendProcessCauses(causes []Cause, procs []process.Info) []Cause {
	if p, ok := topByCPU(procs); ok && p.CPUPct >= 80 {
		causes = append(causes, Cause{"medium", "A process is using heavy CPU",
			fmt.Sprintf("%s (pid %d) at %.0f%% CPU", p.Command, p.PID, p.CPUPct)})
	}
	if p, ok := topByRSS(procs); ok && p.RSSKiB >= 4*1024*1024 {
		causes = append(causes, Cause{"low", "A process is holding a lot of memory",
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

// Render prints the human-readable cause list.
func Render(w io.Writer, causes []Cause) {
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

// reportableFamilies returns the multi-process families for JSON output.
func reportableFamilies(s Signals) []process.Family {
	var out []process.Family
	for _, f := range slowFamilies(s.Procs, s.Now) {
		if f.Kind != process.FamilyOther && f.Members > 1 {
			out = append(out, f)
		}
	}
	return out
}
