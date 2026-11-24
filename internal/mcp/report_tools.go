package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/devices"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawntrace"
)

// These tools answer with exactly the document the matching CLI command
// prints with --json (no summary envelope), built by the same package
// function, so agents parse one shape whichever way they ask. Nothing here
// signals or kills a process.

const (
	loadToolTimeout    = time.Minute
	orphansToolTimeout = 30 * time.Second
	lineageToolTimeout = 30 * time.Second
	devicesToolTimeout = 30 * time.Second
	// spawnsMaxSeconds caps a trace: tools/call blocks the server meanwhile.
	spawnsMaxSeconds     = 30
	spawnsMinIntervalMS  = 50
	spawnsMaxEvents      = 5000
	spawnsTimeoutSlack   = 15 * time.Second
	loadMaxTop           = 50
	idleMaxWindow        = 30 * time.Second
	defaultSpawnsSeconds = 10
)

// ReportDeps are the host seams of the report tools. Each factory is called
// once per tool call; nil fields use the real host.
type ReportDeps struct {
	Load    func() hostload.ReportDeps
	Orphans func() orphans.ReportDeps
	Idle    func() idle.ReportDeps
	Find    owner.FindFunc
	Under   owner.UnderFunc
	Spawns  func() spawntrace.Deps
	Devices func() devices.Options
}

func defaultReportDeps() ReportDeps {
	return ReportDeps{
		Load:    hostload.DefaultReportDeps,
		Orphans: orphans.DefaultReportDeps,
		Idle:    idle.DefaultReportDeps,
		Find:    owner.Finder{}.Find,
		Under:   owner.Finder{}.Under,
		Spawns:  spawntrace.DefaultDeps,
		Devices: func() devices.Options { return devices.Options{} },
	}
}

func mergeReportDeps(c, d ReportDeps) ReportDeps {
	if c.Load == nil {
		c.Load = d.Load
	}
	if c.Orphans == nil {
		c.Orphans = d.Orphans
	}
	if c.Idle == nil {
		c.Idle = d.Idle
	}
	if c.Find == nil {
		c.Find = d.Find
	}
	if c.Under == nil {
		c.Under = d.Under
	}
	if c.Spawns == nil {
		c.Spawns = d.Spawns
	}
	if c.Devices == nil {
		c.Devices = d.Devices
	}
	return c
}

func numberProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "number", "description": description}
}

func reportToolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "load",
			Description: "Can this machine take another job, and if not, who is using it? Same JSON as `spectra load --json`: verdict (admit/hold/backoff), reason, cause, top_cpu/top_rss tables, families, per-root dirs, devices. Ask: \"Should I start another lane?\"",
			InputSchema: objectSchema(map[string]interface{}{
				"roots":            arrayStringProp("Absolute directories holding worktrees; cause is \"roots\" when load leads there."),
				"max_load_per_cpu": numberProp("Hold when 1-minute load is at or above this times the CPU count (default 1.5)."),
				"min_free_gb":      numberProp("Hold when available memory is below this many GiB (default 8)."),
				"backoff_free_gb":  numberProp("Back off when available memory is below this many GiB (default 4)."),
				"top":              integerProp(fmt.Sprintf("Rows per top table (1..%d, default 5).", loadMaxTop)),
			}, nil),
		},
		{
			Name:        "orphans",
			Description: "Leftover processes from worktrees and finished jobs: parent is pid 1 and cwd is under a root. Evidence only, never kills. Same JSON as `spectra orphans --json`: groups (candidates plus daemon_by_design processes), totals, limitations. Ask: \"What did finished lanes leave running?\"",
			InputSchema: objectSchema(map[string]interface{}{
				"roots":     arrayStringProp("Absolute directories holding worktrees (required)."),
				"min_age":   stringProp("Ignore processes younger than this Go duration (default \"1m\")."),
				"no_daemon": boolProp("Do not ask the running daemon for recorded spawn origins."),
			}, []string{"roots"}),
		},
		{
			Name:        "lineage",
			Description: "Ancestors and descendant tree of one pid, with subtree RSS and CPU. Same JSON as `spectra lineage --json`. Ask: \"What started PID 123 and what is under it?\"",
			InputSchema: objectSchema(map[string]interface{}{"pid": integerProp("Process ID (required).")}, []string{"pid"}),
		},
		{
			Name:        "devices",
			Description: "Mobile devices work here may be using: Android devices and emulators adb reaches (USB, emulator, or remote via adb connect), each emulator's AVD name (android.devices[].avd), booted iOS simulators (macOS; every simulator in any state under ios_simulators.all with all_simulators), and emulator/simulator processes attributed to roots. Same JSON as `spectra devices --json`. adb is only asked when an adb server already runs, unless start_adb. Ask: \"Which emulators are booted, which AVD is emulator-5554, and which worktree started them?\"",
			InputSchema: objectSchema(map[string]interface{}{
				"roots":          arrayStringProp("Absolute directories (worktree roots) to attribute emulator and simulator processes to."),
				"start_adb":      boolProp("Ask adb even when no adb server runs. This starts an adb server that keeps running."),
				"all_simulators": boolProp("Also list iOS simulators that are not booted (ios_simulators.all). macOS only."),
			}, nil),
		},
		{
			Name:        "owner",
			Description: "Who has this? Pass target (:PORT, tcp:PORT, udp:PORT, an absolute path, or a pid) for `spectra owner --json`, or under (absolute directories) for `spectra owner --under --json`. Ask: \"Who is listening on 8080?\" \"Is anything still running in this worktree?\"",
			InputSchema: objectSchema(map[string]interface{}{
				"target": stringProp("Port, absolute path, or pid. Exactly one of target or under."),
				"under":  arrayStringProp("Absolute directories: list processes whose cwd is inside them."),
			}, nil),
		},
		{
			Name:        "spawns",
			Description: fmt.Sprintf("Bounded live trace of process starts and exits by polling (blocks for the duration, at most %ds). Returns {events: [...], summary: {...}}: events are the objects `spectra spawns --json` streams one per line, summary is its final line. Ask: \"What keeps spawning under PID 123?\"", spawnsMaxSeconds),
			InputSchema: objectSchema(map[string]interface{}{
				"pid":              integerProp("Only trace this process and its descendants (0 or omitted: all)."),
				"duration_seconds": integerProp(fmt.Sprintf("How long to trace (1..%d, default %d).", spawnsMaxSeconds, defaultSpawnsSeconds)),
				"interval_millis":  integerProp(fmt.Sprintf("Polling interval in ms (%d..duration, default 250).", spawnsMinIntervalMS)),
			}, nil),
		},
		{
			Name:        "idle",
			Description: "Long-idle holders: processes under roots (or named by pids) alive longer than min_age whose tree used under max_cpu percent of one CPU over a window. Evidence only, never signals. Same JSON as `spectra idle --json`. Ask: \"Which lane processes are stuck doing nothing?\"",
			InputSchema: objectSchema(map[string]interface{}{
				"roots":   arrayStringProp("Absolute directories whose processes are checked."),
				"pids":    map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "integer"}, "description": "PIDs to check (with or instead of roots; one of them is required)."},
				"min_age": stringProp("Only processes alive at least this Go duration (default \"10m\")."),
				"max_cpu": numberProp("Idle means process plus descendants used under this percent of one CPU (default 1)."),
				"window":  stringProp(fmt.Sprintf("CPU sample Go duration (100ms..%s, default \"5s\"); the call blocks this long.", idleMaxWindow)),
			}, nil),
		},
	}
}

func (s *Server) reportToolHandlers() map[string]toolHandler {
	return map[string]toolHandler{
		"load":    s.toolLoad,
		"orphans": s.toolOrphans,
		"lineage": s.toolLineage,
		"devices": s.toolDevices,
		"owner":   s.toolOwner,
		"spawns":  s.toolSpawns,
		"idle":    s.toolIdle,
	}
}

// maxRoots bounds how many directories one call may name: each root can cost
// a bounded walk and Spotlight reads, and the server answers one call at a time.
const maxRoots = 32

func absRoots(name string, roots []string) error {
	if len(roots) > maxRoots {
		return fmt.Errorf("%s: at most %d directories", name, maxRoots)
	}
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("%s must be absolute paths: %q", name, r)
		}
	}
	return nil
}

func durationArg(name, v string, def time.Duration) (time.Duration, error) {
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}

func (s *Server) toolLoad(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Roots         []string `json:"roots"`
		MaxLoadPerCPU *float64 `json:"max_load_per_cpu"`
		MinFreeGB     *float64 `json:"min_free_gb"`
		BackoffFreeGB *float64 `json:"backoff_free_gb"`
		Top           *int     `json:"top"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	opts := hostload.DefaultReportOptions()
	opts.Roots = p.Roots
	setIf(&opts.MaxLoadPerCPU, p.MaxLoadPerCPU)
	setIf(&opts.MinFreeGB, p.MinFreeGB)
	setIf(&opts.BackoffFreeGB, p.BackoffFreeGB)
	setIf(&opts.Top, p.Top)
	if err := opts.Validate(); err != nil {
		return toolErrorFrom(err)
	}
	if opts.Top > loadMaxTop {
		return toolError(fmt.Sprintf("top must be 1..%d", loadMaxTop))
	}
	if err := absRoots("roots", opts.Roots); err != nil {
		return toolErrorFrom(err)
	}
	ctx, cancel := context.WithTimeout(ctx, loadToolTimeout)
	defer cancel()
	progressFrom(ctx).step(ctx, "reading load and memory")
	rep, err := hostload.BuildReport(ctx, opts, loadProgress(ctx, s.collect.Reports.Load()))
	if err != nil {
		return toolError("load: " + err.Error())
	}
	return toolText(rep)
}

// loadProgress reports the load report's slow phases (process collection
// and the CPU window) as they start.
func loadProgress(ctx context.Context, deps hostload.ReportDeps) hostload.ReportDeps {
	pr := progressFrom(ctx)
	if pr == nil {
		return deps
	}
	if procs := deps.Procs; procs != nil {
		deps.Procs = func(ctx context.Context) []process.Info {
			pr.step(ctx, "collecting processes")
			return procs(ctx)
		}
	}
	if sleep := deps.Sleep; sleep != nil {
		deps.Sleep = func(ctx context.Context, d time.Duration) {
			pr.step(ctx, fmt.Sprintf("sampling CPU for %s", d))
			sleep(ctx, d)
		}
	}
	return deps
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

func (s *Server) toolOrphans(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Roots    []string `json:"roots"`
		MinAge   string   `json:"min_age"`
		NoDaemon bool     `json:"no_daemon"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	// The server's own cwd is arbitrary, so unlike the CLI there is no
	// current-directory default.
	if len(p.Roots) == 0 {
		return toolError("roots is required")
	}
	if err := absRoots("roots", p.Roots); err != nil {
		return toolErrorFrom(err)
	}
	minAge, err := durationArg("min_age", p.MinAge, orphans.DefaultMinAge)
	if err != nil {
		return toolErrorFrom(err)
	}
	if minAge < 0 {
		return toolError("min_age must not be negative")
	}
	ctx, cancel := context.WithTimeout(ctx, orphansToolTimeout)
	defer cancel()
	rep, err := orphans.BuildReport(ctx, orphans.ReportOptions{Roots: p.Roots, MinAge: minAge, NoDaemon: p.NoDaemon}, s.collect.Reports.Orphans())
	if err != nil {
		return toolError("orphans: " + err.Error())
	}
	return toolText(rep)
}

func (s *Server) toolLineage(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		PID int `json:"pid"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	if p.PID <= 0 {
		return toolError("pid must be a positive integer")
	}
	ctx, cancel := context.WithTimeout(ctx, lineageToolTimeout)
	defer cancel()
	res, err := lineage.Build(s.collect.Processes.CollectProcesses(ctx, process.CollectOptions{}), p.PID)
	if err != nil {
		return toolError("lineage: " + err.Error())
	}
	return toolText(res)
}

func (s *Server) toolOwner(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Target string   `json:"target"`
		Under  []string `json:"under"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	if (p.Target == "") == (len(p.Under) == 0) {
		return toolError("pass exactly one of target or under")
	}
	ctx, cancel := context.WithTimeout(ctx, owner.DefaultTimeout)
	defer cancel()
	if len(p.Under) > 0 {
		if err := absRoots("under", p.Under); err != nil {
			return toolErrorFrom(err)
		}
		res, err := owner.BuildUnderReport(ctx, owner.AbsDirs(p.Under, "/"), s.collect.Reports.Under)
		if err != nil {
			return toolError("owner: " + err.Error())
		}
		return toolText(res)
	}
	t, err := owner.ParseTarget(p.Target, "")
	if err != nil {
		return toolError("owner: " + err.Error())
	}
	if t.Kind == owner.KindPath && !filepath.IsAbs(t.Path) {
		return toolError("owner: a path target must be absolute")
	}
	res, err := owner.BuildReport(ctx, t, s.collect.Reports.Find)
	if err != nil {
		return toolError("owner: " + err.Error())
	}
	return toolText(res)
}

func (s *Server) toolSpawns(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		PID             int  `json:"pid"`
		DurationSeconds *int `json:"duration_seconds"`
		IntervalMillis  *int `json:"interval_millis"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	opts := spawntrace.DefaultOptions()
	opts.PID = p.PID
	opts.Duration = defaultSpawnsSeconds * time.Second
	if p.DurationSeconds != nil {
		if *p.DurationSeconds < 1 || *p.DurationSeconds > spawnsMaxSeconds {
			return toolError(fmt.Sprintf("duration_seconds must be 1..%d", spawnsMaxSeconds))
		}
		opts.Duration = time.Duration(*p.DurationSeconds) * time.Second
	}
	if p.IntervalMillis != nil {
		if *p.IntervalMillis < spawnsMinIntervalMS {
			return toolError(fmt.Sprintf("interval_millis must be at least %d", spawnsMinIntervalMS))
		}
		opts.Interval = time.Duration(*p.IntervalMillis) * time.Millisecond
	}
	if err := opts.Validate(); err != nil {
		return toolError("spawns: " + err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Duration+spawnsTimeoutSlack)
	defer cancel()
	if pr := progressFrom(ctx); pr != nil {
		total := opts.Duration
		opts.Progress = func(elapsed time.Duration, events int) {
			elapsed = min(elapsed, total)
			pr.report(ctx, elapsed.Seconds(), total.Seconds(), fmt.Sprintf("traced %s of %s; %d event(s)", elapsed.Round(100*time.Millisecond), total, events))
		}
	}
	rep, err := spawntrace.Trace(ctx, opts, s.collect.Reports.Spawns(), spawnsMaxEvents)
	if err != nil {
		return toolError("spawns: " + err.Error())
	}
	return toolText(rep)
}

func (s *Server) toolIdle(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Roots  []string `json:"roots"`
		PIDs   []int    `json:"pids"`
		MinAge string   `json:"min_age"`
		MaxCPU *float64 `json:"max_cpu"`
		Window string   `json:"window"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	opts, err := idleOptions(p.Roots, p.PIDs, p.MinAge, p.Window, p.MaxCPU)
	if err != nil {
		return toolErrorFrom(err)
	}
	stop := progressFrom(ctx).elapsed(ctx, opts.Window, "sampling CPU")
	rep, err := idle.BuildReport(ctx, opts, s.collect.Reports.Idle())
	stop()
	if err != nil {
		return toolError("idle: " + err.Error())
	}
	return toolText(rep)
}

// idleOptions applies the CLI defaults and the MCP-only bounds: roots or
// pids are required (no cwd default), roots are absolute, and the window is
// capped because the call blocks for it.
func idleOptions(roots []string, pids []int, minAge, window string, maxCPU *float64) (idle.ReportOptions, error) {
	opts := idle.DefaultReportOptions()
	if len(roots) == 0 && len(pids) == 0 {
		return opts, errors.New("pass roots or pids")
	}
	if err := absRoots("roots", roots); err != nil {
		return opts, err
	}
	opts.Roots, opts.PIDs = roots, pids
	setIf(&opts.MaxCPU, maxCPU)
	var err error
	if opts.MinAge, err = durationArg("min_age", minAge, opts.MinAge); err != nil {
		return opts, err
	}
	if opts.Window, err = durationArg("window", window, opts.Window); err != nil {
		return opts, err
	}
	if opts.Window > idleMaxWindow {
		return opts, fmt.Errorf("window must be at most %s", idleMaxWindow)
	}
	return opts, opts.Validate()
}

func (s *Server) toolDevices(ctx context.Context, raw json.RawMessage) ToolResult {
	var p struct {
		Roots         []string `json:"roots"`
		StartADB      bool     `json:"start_adb"`
		AllSimulators bool     `json:"all_simulators"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if err := absRoots("roots", p.Roots); err != nil {
		return toolError(err.Error())
	}
	opts := s.collect.Reports.Devices()
	opts.StartADB = p.StartADB
	opts.AllSimulators = p.AllSimulators
	opts.Roots = hostload.ResolveRoots(p.Roots, filepath.Abs, filepath.EvalSymlinks)
	ctx, cancel := context.WithTimeout(ctx, devicesToolTimeout)
	defer cancel()
	return toolText(devices.Collect(ctx, opts))
}
