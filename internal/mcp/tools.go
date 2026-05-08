package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/diff"
	issueflow "github.com/kaeawc/spectra/internal/issues"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/toolchain"
)

type toolEnvelope struct {
	Summary     string      `json:"summary"`
	Evidence    []string    `json:"evidence,omitempty"`
	NextActions []string    `json:"next_actions,omitempty"`
	Raw         interface{} `json:"raw,omitempty"`
	Timestamp   time.Time   `json:"timestamp"`
}

func toolDefinitions() []ToolDefinition {
	return []ToolDefinition{
		triageToolDef(),
		inspectAppToolDef(),
		snapshotToolDef(),
		diagnoseToolDef(),
		operationToolDef("process", "Live processes. Ops: list, tree, by_app, sample. Ask: \"What is using memory?\" \"Sample PID 123.\"", []string{"list", "tree", "history", "sample", "by_app"}),
		operationToolDef("jvm", "JVM debug. Ops: list, inspect, explain, thread_dump, gc_stats, vm_memory. Ask: \"Why is PID 123 using heap?\"", []string{"list", "inspect", "explain", "thread_dump", "gc_stats", "vm_memory", "heap_histogram", "heap_dump", "flamegraph", "attach", "mbeans", "mbean_read", "mbean_invoke", "probe"}),
		operationToolDef("network", "Network state and sockets. Ops: state, connections, by_app, diagnose. Ask: \"What is this app connected to?\"", []string{"state", "connections", "by_app", "firewall", "diagnose", "capture_start", "capture_stop"}),
		operationToolDef("toolchain", "Dev tools and drift. Ops: scan, jdk, runtimes, build_tools, brew, drift. Ask: \"Which JDKs are installed?\"", []string{"scan", "jdk", "runtimes", "build_tools", "brew", "drift"}),
		operationToolDef("issues", "Persisted findings. Ops: check, list, acknowledge, dismiss, record_fix, fix_history. Ask: \"What issues are open?\"", []string{"check", "list", "acknowledge", "dismiss", "record_fix", "fix_history"}),
		operationToolDef("remote", "Call a Spectra daemon. Ops: health, rpc, fanout. Ask: \"Check host health.\" \"Run snapshot.create remotely.\"", []string{"health", "rpc", "triage", "fanout"}),
	}
}

func triageToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "triage",
		Description: "Best first tool. Give app_path, pid, bundle_id, or symptom. Returns summary, evidence, next steps. Ask: \"What looks wrong with Slack?\"",
		InputSchema: objectSchema(map[string]interface{}{
			"app_path":    stringProp(".app path."),
			"pid":         integerProp("PID."),
			"bundle_id":   stringProp("Bundle ID."),
			"symptom":     stringProp("Problem to investigate."),
			"network":     boolProp("Scan app URLs."),
			"include_raw": boolProp("Return raw data."),
		}, nil),
	}
}

func inspectAppToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "inspect_app",
		Description: "Inspect .app bundles: runtime, security, helpers, deps, storage, processes. Ask: \"What is this app built with?\"",
		InputSchema: objectSchema(map[string]interface{}{
			"paths":       arrayStringProp(".app paths."),
			"network":     boolProp("Scan app URLs."),
			"deep":        boolProp("Join live process, open-FD/listening-port, and connection state for each app."),
			"include_raw": boolProp("Return raw data."),
		}, []string{"paths"}),
	}
}

func snapshotToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "snapshot",
		Description: "Capture or compare host state. Ops: create, list, get, diff, baseline. Ask: \"What changed since baseline?\"",
		InputSchema: objectSchema(map[string]interface{}{
			"operation":     enumProp([]string{"create", "list", "get", "diff", "baseline"}, "create"),
			"id":            stringProp("Snapshot ID."),
			"id_a":          stringProp("Diff left ID."),
			"id_b":          stringProp("Diff right ID."),
			"name":          stringProp("Baseline name."),
			"network":       boolProp("Scan app URLs."),
			"skip_apps":     boolProp("Skip apps."),
			"store":         boolProp("Save snapshot."),
			"kind":          stringProp("live or baseline."),
			"include_raw":   boolProp("Return raw data."),
			"limit_summary": integerProp("Summary row limit."),
		}, []string{"operation"}),
	}
}

func diagnoseToolDef() ToolDefinition {
	return ToolDefinition{
		Name:        "diagnose",
		Description: "Run rules. Finds risky app, JVM, network, storage, and toolchain state. Ask: \"What needs attention?\"",
		InputSchema: objectSchema(map[string]interface{}{
			"snapshot_id":  stringProp("Snapshot ID."),
			"rules_config": stringProp("spectra.yml path."),
			"persist":      boolProp("Save as issues."),
			"include_raw":  boolProp("Return raw findings."),
		}, nil),
	}
}

func operationToolDef(name, description string, operations []string) ToolDefinition {
	return ToolDefinition{
		Name:        name,
		Description: description,
		InputSchema: objectSchema(map[string]interface{}{
			"operation":         enumProp(operations, operations[0]),
			"pid":               integerProp("PID."),
			"paths":             arrayStringProp("App paths."),
			"bundles":           arrayStringProp("App paths."),
			"snapshot_id":       stringProp("Snapshot ID."),
			"machine_uuid":      stringProp("Machine UUID."),
			"status":            stringProp("Issue status."),
			"id":                stringProp("ID."),
			"issue_id":          stringProp("Issue ID."),
			"duration_seconds":  integerProp("Duration."),
			"interval_millis":   integerProp("Interval."),
			"event":             stringProp("Profiler event."),
			"dest":              stringProp("Output path."),
			"confirm_sensitive": boolProp("Allow sensitive artifact."),
			"deep":              boolProp("More process detail."),
			"include_raw":       boolProp("Return raw data."),
			"network":           stringProp("unix or tcp."),
			"address":           stringProp("Daemon address."),
			"method":            stringProp("Daemon RPC method."),
			"params":            map[string]interface{}{"type": "object", "description": "RPC params."},
			"hosts":             arrayStringProp("Daemon addresses."),
			"command":           stringProp("Fix command."),
			"output":            stringProp("Fix output."),
			"exit_code":         integerProp("Exit code."),
			"applied_by":        stringProp("Actor."),
			"interface":         stringProp("Network interface."),
			"host":              stringProp("Host filter."),
			"port":              integerProp("Port filter."),
			"proto":             stringProp("Protocol."),
			"handle":            stringProp("Capture handle."),
			"limit":             integerProp("Limit."),
		}, []string{"operation"}),
	}
}

func objectSchema(properties map[string]interface{}, required []string) map[string]interface{} {
	out := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": description}
}

func integerProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": description}
}

func boolProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": description}
}

func arrayStringProp(description string) map[string]interface{} {
	return map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": description}
}

func enumProp(values []string, def string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "enum": values, "default": def}
}

func (s *Server) toolTriage(raw json.RawMessage) ToolResult {
	var p triageParams
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}

	evidence, next, rawOut := s.collectTriage(p)
	env := toolEnvelope{
		Summary:     triageSummary(evidence),
		Evidence:    evidence,
		NextActions: next,
		Timestamp:   s.now(),
	}
	if p.IncludeRaw {
		env.Raw = rawOut
	}
	return toolText(env)
}

type triageParams struct {
	AppPath    string `json:"app_path"`
	PID        int    `json:"pid"`
	BundleID   string `json:"bundle_id"`
	Symptom    string `json:"symptom"`
	Network    bool   `json:"network"`
	IncludeRaw bool   `json:"include_raw"`
}

func (s *Server) collectTriage(p triageParams) ([]string, []string, map[string]interface{}) {
	var evidence []string
	var next []string
	rawOut := map[string]interface{}{}
	if p.Symptom != "" {
		evidence = append(evidence, "symptom: "+p.Symptom)
	}
	if p.AppPath != "" {
		evidence, next = s.collectTriageApp(p, evidence, next, rawOut)
	}
	if p.PID > 0 {
		evidence, next = s.collectTriagePID(p.PID, evidence, next, rawOut)
	}
	if p.BundleID != "" {
		evidence = s.collectTriageBundle(p, evidence, rawOut)
	}
	findings, err := s.evaluateLiveRules("")
	if err == nil && len(findings) > 0 {
		evidence = append(evidence, summarizeFindings(findings, 5)...)
		rawOut["findings"] = findings
		next = append(next, "Use diagnose include_raw=true for the full rules output.")
	}
	if len(next) == 0 {
		next = append(next, "Use snapshot operation=create store=true to preserve current state for later diffing.")
	}
	return evidence, next, rawOut
}

func (s *Server) collectTriageApp(p triageParams, evidence, next []string, rawOut map[string]interface{}) ([]string, []string) {
	app, err := s.collect.Apps.InspectApp(p.AppPath, s.detectOptions(p.Network))
	if err != nil {
		evidence = append(evidence, fmt.Sprintf("app inspection failed: %v", err))
	} else {
		evidence = append(evidence, summarizeApp(app)...)
		rawOut["app"] = app
		if app.Runtime == "JVM" || app.Language == "Java" || app.Language == "Kotlin" {
			next = append(next, "Use jvm operation=list or inspect to inspect running JVMs for this app.")
		}
		if len(app.NetworkEndpoints) > 0 {
			next = append(next, "Use network operation=diagnose with host/port when a specific endpoint is failing.")
		}
	}
	procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: []string{p.AppPath}, Deep: true})
	appProcs := filterAppProcesses(procs, p.AppPath)
	if len(appProcs) > 0 {
		evidence = append(evidence, summarizeProcesses(appProcs, 5)...)
		rawOut["processes"] = appProcs
	}
	return evidence, next
}

func (s *Server) collectTriagePID(pid int, evidence, next []string, rawOut map[string]interface{}) ([]string, []string) {
	procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{Deep: true})
	if proc, ok := findProcess(procs, pid); ok {
		evidence = append(evidence, summarizeProcesses([]process.Info{proc}, 1)...)
		rawOut["process"] = proc
	} else {
		evidence = append(evidence, fmt.Sprintf("pid %d not found in process list", pid))
	}
	if info := s.inspectJVM(pid); info != nil {
		evidence = append(evidence, summarizeJVM(*info)...)
		rawOut["jvm"] = info
		next = append(next, "Use jvm operation=explain for GC/thread/memory interpretation.")
	}
	return evidence, next
}

func (s *Server) collectTriageBundle(p triageParams, evidence []string, rawOut map[string]interface{}) []string {
	snap := s.collect.Snapshots.BuildSnapshot(context.Background(), snapshot.Options{SpectraVersion: s.Version, DetectOpts: s.detectOptions(p.Network)})
	for _, app := range snap.Apps {
		if app.BundleID == p.BundleID {
			evidence = append(evidence, summarizeApp(app)...)
			rawOut["matched_app"] = app
			break
		}
	}
	return evidence
}

func (s *Server) toolInspectApp(raw json.RawMessage) ToolResult {
	var p struct {
		Paths      []string `json:"paths"`
		Network    bool     `json:"network"`
		Deep       bool     `json:"deep"`
		IncludeRaw bool     `json:"include_raw"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if len(p.Paths) == 0 {
		return toolError("paths is required")
	}
	results := make([]inspectResult, 0, len(p.Paths))
	evidence := []string{}
	for _, path := range p.Paths {
		r, err := s.collect.Apps.InspectApp(path, s.detectOptions(p.Network))
		item := inspectResult{Path: path}
		if err != nil {
			item.Error = err.Error()
			evidence = append(evidence, fmt.Sprintf("%s: %v", path, err))
		} else {
			item.Success = true
			item.Result = r
			evidence = append(evidence, summarizeApp(r)...)
			if p.Deep {
				deep := s.deepInspectApp(path, r)
				item.Deep = &deep
				evidence = append(evidence, summarizeDeepInspect(path, deep)...)
			}
		}
		results = append(results, item)
	}
	env := toolEnvelope{
		Summary:     fmt.Sprintf("inspected %d app path(s)", len(p.Paths)),
		Evidence:    evidence,
		NextActions: []string{"Use triage with app_path for prioritized process/network/rules context."},
		Timestamp:   s.now(),
	}
	if p.IncludeRaw || p.Deep {
		env.Raw = results
	}
	return toolText(env)
}

func (s *Server) toolSnapshot(raw json.RawMessage) ToolResult {
	var p snapshotParams
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if p.Operation == "" {
		p.Operation = "create"
	}
	switch p.Operation {
	case "create", "baseline":
		return s.toolSnapshotCreate(p)
	case "list":
		return s.toolSnapshotList(p)
	case "get":
		return s.toolSnapshotGet(p)
	case "diff":
		return s.toolSnapshotDiff(p)
	default:
		return toolError("unknown snapshot operation: " + p.Operation)
	}
}

type snapshotParams struct {
	Operation    string `json:"operation"`
	ID           string `json:"id"`
	IDA          string `json:"id_a"`
	IDB          string `json:"id_b"`
	Name         string `json:"name"`
	Network      bool   `json:"network"`
	SkipApps     bool   `json:"skip_apps"`
	Store        bool   `json:"store"`
	Kind         string `json:"kind"`
	IncludeRaw   bool   `json:"include_raw"`
	LimitSummary int    `json:"limit_summary"`
}

func (s *Server) toolSnapshotCreate(p snapshotParams) ToolResult {
	snap := s.collect.Snapshots.BuildSnapshot(context.Background(), snapshot.Options{
		SpectraVersion: s.Version,
		DetectOpts:     s.detectOptions(p.Network),
		SkipApps:       p.SkipApps,
	})
	if p.Operation == "baseline" {
		snap.Kind = snapshot.KindBaseline
	}
	if p.Store {
		if err := saveSnapshot(snap); err != nil {
			return toolError(err.Error())
		}
	}
	evidence := summarizeSnapshot(snap, p.LimitSummary)
	if p.Store {
		evidence = append(evidence, "snapshot persisted")
	}
	env := toolEnvelope{
		Summary:     fmt.Sprintf("created %s snapshot %s", snap.Kind, snap.ID),
		Evidence:    evidence,
		NextActions: []string{"Use snapshot operation=diff against a baseline to inspect changes."},
		Timestamp:   s.now(),
	}
	if p.IncludeRaw {
		env.Raw = snap
	}
	return toolText(env)
}

func (s *Server) toolSnapshotList(p snapshotParams) ToolResult {
	db, err := openStore()
	if err != nil {
		return toolError(err.Error())
	}
	defer db.Close()
	rows, err := db.ListSnapshots(context.Background(), p.Kind)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d stored snapshot(s)", len(rows)), Raw: rows, Timestamp: s.now()})
}

func (s *Server) toolSnapshotGet(p snapshotParams) ToolResult {
	if p.ID == "" {
		return toolError("snapshot get requires id")
	}
	snap, err := loadStoredSnapshot(p.ID)
	if err != nil {
		return toolError(err.Error())
	}
	env := toolEnvelope{Summary: "loaded snapshot " + p.ID, Evidence: summarizeSnapshot(snap, p.LimitSummary), Timestamp: s.now()}
	if p.IncludeRaw {
		env.Raw = snap
	}
	return toolText(env)
}

func (s *Server) toolSnapshotDiff(p snapshotParams) ToolResult {
	if p.IDA == "" || p.IDB == "" {
		return toolError("snapshot diff requires id_a and id_b")
	}
	a, err := loadStoredSnapshot(p.IDA)
	if err != nil {
		return toolError(err.Error())
	}
	b, err := loadStoredSnapshot(p.IDB)
	if err != nil {
		return toolError(err.Error())
	}
	result := diff.Compare(a, b)
	return toolText(toolEnvelope{Summary: fmt.Sprintf("diffed %s against %s", p.IDA, p.IDB), Raw: result, Timestamp: s.now()})
}

func (s *Server) toolDiagnose(raw json.RawMessage) ToolResult {
	var p struct {
		SnapshotID  string `json:"snapshot_id"`
		RulesConfig string `json:"rules_config"`
		Persist     bool   `json:"persist"`
		IncludeRaw  bool   `json:"include_raw"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	snap, findings, err := s.evaluateRules(p.SnapshotID, p.RulesConfig)
	if err != nil {
		return toolError(err.Error())
	}
	evidence := summarizeFindings(findings, 10)
	next := []string{"Use issues operation=check to persist these findings if they should be tracked."}
	if p.Persist {
		ids, err := persistFindings(snap, findings)
		if err != nil {
			return toolError(err.Error())
		}
		evidence = append(evidence, fmt.Sprintf("persisted %d issue(s)", len(ids)))
		next = []string{"Use issues operation=list with this machine_uuid to inspect persisted issue lifecycle."}
	}
	env := toolEnvelope{Summary: fmt.Sprintf("%d finding(s)", len(findings)), Evidence: evidence, NextActions: next, Timestamp: s.now()}
	if p.IncludeRaw {
		env.Raw = findings
	}
	return toolText(env)
}

func (s *Server) toolProcess(raw json.RawMessage) ToolResult {
	var p struct {
		Operation       string   `json:"operation"`
		PID             int      `json:"pid"`
		Bundles         []string `json:"bundles"`
		Paths           []string `json:"paths"`
		Deep            bool     `json:"deep"`
		Limit           int      `json:"limit"`
		DurationSeconds int      `json:"duration_seconds"`
		IntervalMillis  int      `json:"interval_millis"`
		IncludeRaw      bool     `json:"include_raw"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	bundles := append([]string{}, p.Bundles...)
	bundles = append(bundles, p.Paths...)
	switch p.Operation {
	case "list", "":
		procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: bundles, Deep: p.Deep})
		sortProcesses(procs)
		if p.Limit > 0 && p.Limit < len(procs) {
			procs = procs[:p.Limit]
		}
		return toolText(toolEnvelope{Summary: fmt.Sprintf("collected %d process(es)", len(procs)), Evidence: summarizeProcesses(procs, 10), Raw: optionalRaw(p.IncludeRaw, procs), Timestamp: s.now()})
	case "tree":
		procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: bundles})
		tree := process.BuildTree(procs)
		return toolText(toolEnvelope{Summary: "built process tree", Raw: tree, Timestamp: s.now()})
	case "by_app":
		if len(bundles) == 0 {
			return toolError("process by_app requires paths or bundles")
		}
		procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: bundles, Deep: p.Deep})
		return toolText(toolEnvelope{Summary: fmt.Sprintf("collected processes for %d app bundle(s)", len(bundles)), Evidence: summarizeProcesses(procs, 20), Raw: optionalRaw(p.IncludeRaw, procs), Timestamp: s.now()})
	case "sample":
		if p.PID == 0 {
			return toolError("process sample requires pid")
		}
		duration := p.DurationSeconds
		if duration <= 0 {
			duration = 1
		}
		interval := p.IntervalMillis
		if interval <= 0 {
			interval = 10
		}
		out, err := s.collect.Processes.SampleProcess(p.PID, duration, interval)
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(toolEnvelope{Summary: fmt.Sprintf("sampled pid %d for %ds", p.PID, duration), Raw: map[string]interface{}{"pid": p.PID, "output": out}, Timestamp: s.now()})
	case "history":
		return toolError("process history requires a running spectra daemon; use remote operation=rpc method=process.history")
	default:
		return toolError("unknown process operation: " + p.Operation)
	}
}

func (s *Server) toolJVM(raw json.RawMessage) ToolResult {
	var p jvmParams
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	op := p.Operation
	if op == "" {
		op = "list"
	}
	handlers := map[string]func(jvmParams) ToolResult{
		"list":           s.toolJVMList,
		"inspect":        s.toolJVMInspect,
		"explain":        s.toolJVMExplain,
		"thread_dump":    func(p jvmParams) ToolResult { return s.jcmdText(p.PID, "thread dump", s.collect.JVMs.ThreadDump) },
		"gc_stats":       s.toolJVMGCStats,
		"vm_memory":      s.toolJVMVMMemory,
		"heap_histogram": func(p jvmParams) ToolResult { return s.jcmdText(p.PID, "heap histogram", s.collect.JVMs.HeapHistogram) },
		"attach":         s.toolJVMAttach,
		"mbeans":         s.toolJVMMBeans,
		"mbean_read":     s.toolJVMMBeanRead,
		"mbean_invoke":   s.toolJVMMBeanInvoke,
		"probe":          s.toolJVMProbe,
		"heap_dump":      s.toolJVMHeapDump,
		"flamegraph":     s.toolJVMFlamegraph,
	}
	handler, ok := handlers[op]
	if !ok {
		return toolError("unknown jvm operation: " + p.Operation)
	}
	return handler(p)
}

type jvmParams struct {
	Operation        string `json:"operation"`
	PID              int    `json:"pid"`
	Samples          int    `json:"samples"`
	IntervalMillis   int    `json:"interval_millis"`
	Dest             string `json:"dest"`
	Event            string `json:"event"`
	DurationSeconds  int    `json:"duration_seconds"`
	ConfirmSensitive bool   `json:"confirm_sensitive"`
	AsprofPath       string `json:"asprof_path"`
	Agent            string `json:"agent"`
	MBeanName        string `json:"mbean_name"`
	Attribute        string `json:"attribute"`
	MBeanOperation   string `json:"mbean_operation"`
	IncludeRaw       bool   `json:"include_raw"`
}

func (s *Server) toolJVMList(p jvmParams) ToolResult {
	jvms := s.collect.JVMs.CollectJVMs(context.Background(), s.jvmOptions())
	return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d JVM process(es)", len(jvms)), Evidence: summarizeJVMs(jvms), Raw: optionalRaw(p.IncludeRaw, jvms), Timestamp: s.now()})
}

func (s *Server) toolJVMInspect(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm inspect requires pid")
	}
	info := s.inspectJVM(p.PID)
	if info == nil {
		return toolError(fmt.Sprintf("JVM pid %d not found", p.PID))
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("inspected JVM pid %d", p.PID), Evidence: summarizeJVM(*info), Raw: optionalRaw(p.IncludeRaw, info), Timestamp: s.now()})
}

func (s *Server) toolJVMExplain(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm explain requires pid")
	}
	explanation, err := s.collect.JVMs.CollectExplanation(context.Background(), p.PID, jvm.ExplainOptions{Samples: p.Samples, Interval: time.Duration(p.IntervalMillis) * time.Millisecond, SoftRefs: true})
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("explained JVM pid %d", p.PID), Raw: explanation, Timestamp: s.now()})
}

func (s *Server) toolJVMGCStats(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm gc_stats requires pid")
	}
	stats, err := s.collect.JVMs.CollectGCStats(p.PID)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("collected GC stats for pid %d", p.PID), Raw: stats, Timestamp: s.now()})
}

func (s *Server) toolJVMVMMemory(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm vm_memory requires pid")
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("collected VM memory diagnostics for pid %d", p.PID), Raw: s.collect.JVMs.CollectVMMemoryDiagnostics(p.PID), Timestamp: s.now()})
}

func (s *Server) toolJVMAttach(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm attach requires pid")
	}
	status, err := jvm.AttachAgent(p.PID, p.Agent, nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("attached spectra agent to pid %d", p.PID), Raw: status, Timestamp: s.now()})
}

func (s *Server) toolJVMMBeans(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm mbeans requires pid")
	}
	mbeans, err := jvm.FetchMBeans(p.PID, nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("listed %d MBean(s) for pid %d", len(mbeans.MBeans), p.PID), Raw: optionalRaw(p.IncludeRaw, mbeans), Timestamp: s.now()})
}

func (s *Server) toolJVMMBeanRead(p jvmParams) ToolResult {
	if p.PID == 0 || p.MBeanName == "" || p.Attribute == "" {
		return toolError("jvm mbean_read requires pid, mbean_name, and attribute")
	}
	value, err := jvm.ReadMBeanAttribute(p.PID, p.MBeanName, p.Attribute, nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("read MBean attribute %s on pid %d", p.Attribute, p.PID), Raw: value, Timestamp: s.now()})
}

func (s *Server) toolJVMMBeanInvoke(p jvmParams) ToolResult {
	if p.PID == 0 || p.MBeanName == "" || p.MBeanOperation == "" {
		return toolError("jvm mbean_invoke requires pid, mbean_name, and mbean_operation")
	}
	value, err := jvm.InvokeMBeanOperation(p.PID, p.MBeanName, p.MBeanOperation, nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("invoked MBean operation %s on pid %d", p.MBeanOperation, p.PID), Raw: value, Timestamp: s.now()})
}

func (s *Server) toolJVMProbe(p jvmParams) ToolResult {
	if p.PID == 0 {
		return toolError("jvm probe requires pid")
	}
	probes, err := jvm.FetchAgentProbes(p.PID, nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("collected in-process probes for pid %d", p.PID), Raw: probes, Timestamp: s.now()})
}

func (s *Server) toolJVMHeapDump(p jvmParams) ToolResult {
	if !p.ConfirmSensitive {
		return toolError("jvm heap_dump requires confirm_sensitive=true")
	}
	if p.PID == 0 {
		return toolError("jvm heap_dump requires pid")
	}
	if p.Dest == "" {
		p.Dest = fmt.Sprintf("/tmp/spectra-heap-%d.hprof", p.PID)
	}
	if err := s.collect.JVMs.HeapDump(p.PID, p.Dest); err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("wrote heap dump for pid %d", p.PID), Raw: map[string]interface{}{"pid": p.PID, "dest": p.Dest}, Timestamp: s.now()})
}

func (s *Server) toolJVMFlamegraph(p jvmParams) ToolResult {
	if !p.ConfirmSensitive {
		return toolError("jvm flamegraph requires confirm_sensitive=true")
	}
	if p.PID == 0 {
		return toolError("jvm flamegraph requires pid")
	}
	if p.Dest == "" {
		p.Dest = fmt.Sprintf("/tmp/spectra-flamegraph-%d.html", p.PID)
	}
	err := s.collect.JVMs.CaptureFlamegraph(p.PID, jvm.FlamegraphOptions{AsprofPath: p.AsprofPath, Event: p.Event, DurationSeconds: p.DurationSeconds, OutputPath: p.Dest})
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("wrote flamegraph for pid %d", p.PID), Raw: map[string]interface{}{"pid": p.PID, "dest": p.Dest}, Timestamp: s.now()})
}

func (s *Server) toolNetwork(raw json.RawMessage) ToolResult {
	var p struct {
		Operation  string   `json:"operation"`
		Bundles    []string `json:"bundles"`
		Paths      []string `json:"paths"`
		Host       string   `json:"host"`
		Port       int      `json:"port"`
		IncludeRaw bool     `json:"include_raw"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	switch p.Operation {
	case "state", "":
		state := s.collect.Network.CollectNetworkState()
		return toolText(toolEnvelope{Summary: summarizeNetworkState(state), Evidence: networkEvidence(state), Raw: optionalRaw(p.IncludeRaw, state), Timestamp: s.now()})
	case "connections":
		conns := s.collect.Network.CollectConnections()
		return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d active connection(s)", len(conns)), Raw: optionalRaw(true, conns), Timestamp: s.now()})
	case "by_app":
		bundles := append([]string{}, p.Bundles...)
		bundles = append(bundles, p.Paths...)
		conns := s.collect.Network.CollectConnections()
		procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: bundles})
		grouped := netstate.GroupConnectionsByApp(conns, procs)
		return toolText(toolEnvelope{Summary: fmt.Sprintf("grouped %d connection(s) by app", len(conns)), Raw: grouped, Timestamp: s.now()})
	case "diagnose":
		state := s.collect.Network.CollectNetworkState()
		conns := s.collect.Network.CollectConnections()
		evidence := networkEvidence(state)
		if p.Host != "" {
			evidence = append(evidence, connectionEvidenceForHost(conns, p.Host)...)
		}
		if p.Port > 0 {
			evidence = append(evidence, connectionEvidenceForPort(conns, p.Port)...)
		}
		return toolText(toolEnvelope{Summary: "network diagnostic snapshot collected", Evidence: evidence, Raw: optionalRaw(p.IncludeRaw, map[string]interface{}{"state": state, "connections": conns}), Timestamp: s.now()})
	case "firewall", "capture_start", "capture_stop":
		return toolError("network " + p.Operation + " requires the privileged helper via a running spectra daemon; use remote operation=rpc")
	default:
		return toolError("unknown network operation: " + p.Operation)
	}
}

func (s *Server) toolToolchain(raw json.RawMessage) ToolResult {
	var p struct {
		Operation  string `json:"operation"`
		IncludeRaw bool   `json:"include_raw"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	tc := s.collect.Toolchain.CollectToolchains(context.Background(), toolchain.CollectOptions{})
	switch p.Operation {
	case "scan", "":
		return toolText(toolEnvelope{Summary: summarizeToolchains(tc), Raw: optionalRaw(p.IncludeRaw, tc), Timestamp: s.now()})
	case "jdk":
		return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d JDK install(s)", len(tc.JDKs)), Raw: tc.JDKs, Timestamp: s.now()})
	case "runtimes":
		return toolText(toolEnvelope{Summary: "collected language runtimes", Raw: map[string]interface{}{"node": tc.Node, "python": tc.Python, "go": tc.Go, "ruby": tc.Ruby, "rust": tc.Rust}, Timestamp: s.now()})
	case "build_tools":
		return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d build tool install(s)", len(tc.BuildTools)), Raw: tc.BuildTools, Timestamp: s.now()})
	case "brew":
		return toolText(toolEnvelope{Summary: fmt.Sprintf("brew: %d formulae, %d casks, %d taps", len(tc.Brew.Formulae), len(tc.Brew.Casks), len(tc.Brew.Taps)), Raw: tc.Brew, Timestamp: s.now()})
	case "drift":
		findings := toolchainDriftEvidence(tc)
		return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d toolchain drift signal(s)", len(findings)), Evidence: findings, Raw: optionalRaw(p.IncludeRaw, tc), Timestamp: s.now()})
	default:
		return toolError("unknown toolchain operation: " + p.Operation)
	}
}

func (s *Server) toolIssues(raw json.RawMessage) ToolResult {
	var p issuesParams
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	db, err := openStore()
	if err != nil {
		return toolError(err.Error())
	}
	defer db.Close()
	switch p.Operation {
	case "check", "":
		return s.toolIssuesCheck(db, p)
	case "list":
		return s.toolIssuesList(db, p)
	case "acknowledge", "dismiss":
		return s.toolIssuesUpdateStatus(db, p)
	case "record_fix":
		return s.toolIssuesRecordFix(db, p)
	case "fix_history":
		return s.toolIssuesFixHistory(db, p)
	default:
		return toolError("unknown issues operation: " + p.Operation)
	}
}

type issuesParams struct {
	Operation   string `json:"operation"`
	SnapshotID  string `json:"snapshot_id"`
	MachineUUID string `json:"machine_uuid"`
	Status      string `json:"status"`
	ID          string `json:"id"`
	IssueID     string `json:"issue_id"`
	AppliedBy   string `json:"applied_by"`
	Command     string `json:"command"`
	Output      string `json:"output"`
	ExitCode    int    `json:"exit_code"`
}

func (s *Server) toolIssuesCheck(db *store.DB, p issuesParams) ToolResult {
	snap, findings, err := s.evaluateRules(p.SnapshotID, "")
	if err != nil {
		return toolError(err.Error())
	}
	ids, err := persistFindingsWithDB(db, snap, findings)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("persisted %d issue(s)", len(ids)), Evidence: summarizeFindings(findings, 10), Raw: map[string]interface{}{"snapshot_id": snap.ID, "ids": ids}, Timestamp: s.now()})
}

func (s *Server) toolIssuesList(db *store.DB, p issuesParams) ToolResult {
	if p.MachineUUID == "" {
		return toolError("issues list requires machine_uuid")
	}
	rows, err := db.ListIssues(context.Background(), p.MachineUUID, store.IssueStatus(p.Status))
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d issue(s)", len(rows)), Raw: rows, Timestamp: s.now()})
}

func (s *Server) toolIssuesUpdateStatus(db *store.DB, p issuesParams) ToolResult {
	if p.ID == "" {
		return toolError("issues " + p.Operation + " requires id")
	}
	status := store.IssueAcknowledged
	if p.Operation == "dismiss" {
		status = store.IssueDismissed
	}
	svc := issueflow.Service{Store: db}
	if err := svc.Update(context.Background(), p.ID, status); err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("set issue %s to %s", p.ID, status), Timestamp: s.now()})
}

func (s *Server) toolIssuesRecordFix(db *store.DB, p issuesParams) ToolResult {
	if p.IssueID == "" {
		return toolError("issues record_fix requires issue_id")
	}
	svc := issueflow.Service{Store: db}
	id, err := svc.RecordFix(context.Background(), store.AppliedFixInput{IssueID: p.IssueID, AppliedBy: p.AppliedBy, Command: p.Command, Output: p.Output, ExitCode: p.ExitCode})
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: "recorded applied fix", Raw: map[string]interface{}{"id": id}, Timestamp: s.now()})
}

func (s *Server) toolIssuesFixHistory(db *store.DB, p issuesParams) ToolResult {
	if p.IssueID == "" {
		return toolError("issues fix_history requires issue_id")
	}
	svc := issueflow.Service{Store: db}
	rows, err := svc.FixHistory(context.Background(), p.IssueID)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("found %d fix attempt(s)", len(rows)), Raw: rows, Timestamp: s.now()})
}

func (s *Server) toolRemote(raw json.RawMessage) ToolResult {
	var p remoteParams
	if err := decodeArgs(raw, &p); err != nil {
		return toolError(err.Error())
	}
	if p.Network == "" {
		p.Network = "tcp"
	}
	switch p.Operation {
	case "health", "":
		return s.toolRemoteHealth(p)
	case "rpc":
		return s.toolRemoteRPC(p)
	case "triage":
		return toolError("remote triage is not a daemon RPC yet; use remote operation=rpc with method names such as snapshot.create, rules.check, jvm.list")
	case "fanout":
		return s.toolRemoteFanout(p)
	default:
		return toolError("unknown remote operation: " + p.Operation)
	}
}

type remoteParams struct {
	Operation string                 `json:"operation"`
	Network   string                 `json:"network"`
	Address   string                 `json:"address"`
	Method    string                 `json:"method"`
	Params    map[string]interface{} `json:"params"`
	Hosts     []string               `json:"hosts"`
}

func (s *Server) toolRemoteHealth(p remoteParams) ToolResult {
	address := p.Address
	if address == "" {
		address = "127.0.0.1:7878"
	}
	resp, err := callDaemon(p.Network, address, "health", nil)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: "remote health check completed", Raw: resp, Timestamp: s.now()})
}

func (s *Server) toolRemoteRPC(p remoteParams) ToolResult {
	if p.Address == "" || p.Method == "" {
		return toolError("remote rpc requires address and method")
	}
	resp, err := callDaemon(p.Network, p.Address, p.Method, p.Params)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: "remote rpc completed: " + p.Method, Raw: resp, Timestamp: s.now()})
}

func (s *Server) toolRemoteFanout(p remoteParams) ToolResult {
	if len(p.Hosts) == 0 || p.Method == "" {
		return toolError("remote fanout requires hosts and method")
	}
	out := make(map[string]interface{}, len(p.Hosts))
	for _, host := range p.Hosts {
		resp, err := callDaemon(p.Network, host, p.Method, p.Params)
		if err != nil {
			out[host] = map[string]string{"error": err.Error()}
		} else {
			out[host] = resp
		}
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("fanout completed for %d host(s)", len(p.Hosts)), Raw: out, Timestamp: s.now()})
}

type inspectResult struct {
	Path    string             `json:"path"`
	Success bool               `json:"success"`
	Result  detect.Result      `json:"result,omitempty"`
	Deep    *deepInspectResult `json:"deep,omitempty"`
	Error   string             `json:"error,omitempty"`
}

type deepInspectResult struct {
	Processes        []process.Info                          `json:"processes,omitempty"`
	Connections      []netstate.AttributedConnection         `json:"connections,omitempty"`
	ListeningPorts   []netstate.ListeningPort                `json:"listening_ports,omitempty"`
	ProcessTotals    deepProcessTotals                       `json:"process_totals"`
	Security         deepSecuritySummary                     `json:"security"`
	MissingSignals   []string                                `json:"missing_signals,omitempty"`
	ConnectionsByPID map[int][]netstate.AttributedConnection `json:"connections_by_pid,omitempty"`
}

type deepProcessTotals struct {
	Count       int     `json:"count"`
	RSSKiB      int64   `json:"rss_kib"`
	OpenFDs     int     `json:"open_fds,omitempty"`
	CPUPercent  float64 `json:"cpu_pct,omitempty"`
	ThreadCount int     `json:"thread_count,omitempty"`
}

type deepSecuritySummary struct {
	TeamID             string   `json:"team_id,omitempty"`
	HardenedRuntime    bool     `json:"hardened_runtime"`
	Sandboxed          bool     `json:"sandboxed"`
	Entitlements       []string `json:"entitlements,omitempty"`
	GrantedPermissions []string `json:"granted_permissions,omitempty"`
	GatekeeperStatus   string   `json:"gatekeeper_status,omitempty"`
}

func (s *Server) deepInspectApp(appPath string, app detect.Result) deepInspectResult {
	procs := s.collect.Processes.CollectProcesses(context.Background(), process.CollectOptions{BundlePaths: []string{appPath}, Deep: true})
	appProcs := filterAppProcesses(procs, appPath)
	sortProcesses(appProcs)

	conns := s.collect.Network.CollectConnections()
	grouped := netstate.GroupConnectionsByApp(conns, appProcs)
	attributed := append([]netstate.AttributedConnection{}, grouped[appPath]...)
	sort.SliceStable(attributed, func(i, j int) bool {
		if attributed[i].PID != attributed[j].PID {
			return attributed[i].PID < attributed[j].PID
		}
		return attributed[i].RemoteAddr < attributed[j].RemoteAddr
	})

	state := s.collect.Network.CollectNetworkState()
	listening := listeningPortsForProcesses(state.ListeningPorts, appPath, appProcs)

	out := deepInspectResult{
		Processes:        appProcs,
		Connections:      attributed,
		ListeningPorts:   listening,
		ProcessTotals:    totalProcesses(appProcs),
		ConnectionsByPID: groupConnectionsByPID(attributed),
		Security: deepSecuritySummary{
			TeamID:             app.TeamID,
			HardenedRuntime:    app.HardenedRuntime,
			Sandboxed:          app.Sandboxed,
			Entitlements:       append([]string{}, app.Entitlements...),
			GrantedPermissions: append([]string{}, app.GrantedPermissions...),
			GatekeeperStatus:   app.GatekeeperStatus,
		},
	}
	if len(appProcs) == 0 {
		out.MissingSignals = append(out.MissingSignals, "no running processes matched this app bundle")
	}
	if len(attributed) == 0 {
		out.MissingSignals = append(out.MissingSignals, "no active app-attributed connections found")
	}
	return out
}

func summarizeDeepInspect(appPath string, deep deepInspectResult) []string {
	evidence := []string{
		fmt.Sprintf("%s live: processes=%d rss=%dKiB cpu=%.1f open_fds=%d listening=%d connections=%d",
			appPath, deep.ProcessTotals.Count, deep.ProcessTotals.RSSKiB, deep.ProcessTotals.CPUPercent, deep.ProcessTotals.OpenFDs, len(deep.ListeningPorts), len(deep.Connections)),
	}
	if len(deep.MissingSignals) > 0 {
		evidence = append(evidence, "missing: "+strings.Join(deep.MissingSignals, "; "))
	}
	return evidence
}

func totalProcesses(procs []process.Info) deepProcessTotals {
	var total deepProcessTotals
	total.Count = len(procs)
	for _, p := range procs {
		total.RSSKiB += p.RSSKiB
		total.OpenFDs += p.OpenFDs
		total.CPUPercent += p.CPUPct
		total.ThreadCount += p.ThreadCount
	}
	return total
}

func listeningPortsForProcesses(ports []netstate.ListeningPort, appPath string, procs []process.Info) []netstate.ListeningPort {
	pids := make(map[int]bool, len(procs))
	for _, p := range procs {
		pids[p.PID] = true
	}
	out := make([]netstate.ListeningPort, 0)
	for _, port := range ports {
		if port.AppPath == appPath || (port.PID > 0 && pids[port.PID]) {
			out = append(out, port)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

func groupConnectionsByPID(conns []netstate.AttributedConnection) map[int][]netstate.AttributedConnection {
	if len(conns) == 0 {
		return nil
	}
	out := make(map[int][]netstate.AttributedConnection)
	for _, conn := range conns {
		if conn.PID > 0 {
			out[conn.PID] = append(out[conn.PID], conn)
		}
	}
	return out
}

func (s *Server) detectOptions(scanNetwork bool) detect.Options {
	return detect.Options{ScanNetwork: scanNetwork, Now: s.collect.Clock.Now}
}

func (s *Server) now() time.Time {
	return s.collect.Clock.Now().UTC()
}

func decodeArgs(raw json.RawMessage, dst interface{}) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func toolText(value interface{}) ToolResult {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("marshal tool result: %v", err))
	}
	return ToolResult{Content: []ContentBlock{{Type: "text", Text: string(payload)}}}
}

func toolError(msg string) ToolResult {
	return ToolResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: msg}}}
}

func optionalRaw(include bool, value interface{}) interface{} {
	if include {
		return value
	}
	return nil
}

func openStore() (*store.DB, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	return store.Open(dbPath)
}

func resolveRulesCatalog(path string) ([]rules.Rule, error) {
	base := rules.V1Catalog()
	if path == "" {
		if _, err := os.Stat("spectra.yml"); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return base, nil
			}
			return nil, err
		}
		path = "spectra.yml"
	}
	overrides, err := rules.LoadOverrides(path)
	if err != nil {
		return nil, err
	}
	return rules.ApplyOverrides(base, overrides), nil
}

func loadStoredSnapshot(id string) (snapshot.Snapshot, error) {
	var empty snapshot.Snapshot
	db, err := openStore()
	if err != nil {
		return empty, err
	}
	defer db.Close()
	raw, err := db.GetSnapshotJSON(context.Background(), id)
	if err != nil {
		return empty, err
	}
	if len(raw) == 0 {
		return empty, fmt.Errorf("snapshot %q has no JSON payload", id)
	}
	if err := json.Unmarshal(raw, &empty); err != nil {
		return empty, fmt.Errorf("unmarshal snapshot %q: %w", id, err)
	}
	return empty, nil
}

func saveSnapshot(snap snapshot.Snapshot) error {
	db, err := openStore()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.SaveSnapshot(context.Background(), store.FromSnapshot(snap)); err != nil {
		return err
	}
	_ = db.SaveSnapshotProcesses(context.Background(), snap.ID, store.ProcessesFromSnapshot(snap))
	_ = db.SaveLoginItems(context.Background(), snap.ID, store.LoginItemsFromSnapshot(snap))
	_ = db.SaveGrantedPerms(context.Background(), snap.ID, store.GrantedPermsFromSnapshot(snap))
	return nil
}

func (s *Server) evaluateLiveRules(config string) ([]rules.Finding, error) {
	_, findings, err := s.evaluateRules("", config)
	return findings, err
}

func (s *Server) evaluateRules(snapshotID, config string) (snapshot.Snapshot, []rules.Finding, error) {
	var snap snapshot.Snapshot
	var err error
	if snapshotID != "" {
		snap, err = loadStoredSnapshot(snapshotID)
		if err != nil {
			return snap, nil, err
		}
	} else {
		snap = s.collect.Snapshots.BuildSnapshot(context.Background(), snapshot.Options{SpectraVersion: s.Version})
	}
	catalog, err := resolveRulesCatalog(config)
	if err != nil {
		return snap, nil, err
	}
	// Best-effort: persist current JVMs as samples and read recent history
	// back into the snapshot so trend-aware rules see a multi-sample window.
	// Failure here is non-fatal; rules degrade to point-in-time checks.
	attachJVMHistory(&snap)
	return snap, rules.Evaluate(snap, catalog), nil
}

// attachJVMHistory opens the local samples store, calls AttachJVMHistory,
// and closes the connection. Failure is silent — history is an enhancement,
// not a contract.
func attachJVMHistory(snap *snapshot.Snapshot) {
	if len(snap.JVMs) == 0 {
		return
	}
	db, err := openStore()
	if err != nil {
		return
	}
	defer db.Close()
	db.AttachJVMHistory(context.Background(), snap)
}

func persistFindings(snap snapshot.Snapshot, findings []rules.Finding) ([]string, error) {
	db, err := openStore()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return persistFindingsWithDB(db, snap, findings)
}

func persistFindingsWithDB(db *store.DB, snap snapshot.Snapshot, findings []rules.Finding) ([]string, error) {
	if err := db.SaveSnapshot(context.Background(), store.FromSnapshot(snap)); err != nil {
		return nil, err
	}
	inputs := make([]store.FindingInput, 0, len(findings))
	for _, f := range findings {
		inputs = append(inputs, store.FindingInput{RuleID: f.RuleID, Subject: f.Subject, Severity: string(f.Severity), Message: f.Message, Fix: f.Fix})
	}
	machineUUID := snap.Host.MachineUUID
	if machineUUID == "" {
		machineUUID = snap.Host.Hostname
	}
	if machineUUID == "" {
		machineUUID = "local"
	}
	return db.UpsertIssues(context.Background(), machineUUID, snap.ID, inputs)
}

func (s *Server) jvmOptions() jvm.CollectOptions {
	tc := s.collect.Toolchain.CollectToolchains(context.Background(), toolchain.CollectOptions{})
	return jvm.CollectOptions{JDKs: tc.JDKs}
}

func (s *Server) inspectJVM(pid int) *jvm.Info {
	return s.collect.JVMs.InspectJVM(context.Background(), pid, s.jvmOptions())
}

func (s *Server) jcmdText(pid int, label string, fn func(int) ([]byte, error)) ToolResult {
	if pid == 0 {
		return toolError("jvm " + label + " requires pid")
	}
	out, err := fn(pid)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(toolEnvelope{Summary: fmt.Sprintf("collected %s for pid %d", label, pid), Raw: map[string]interface{}{"pid": pid, "output": string(out)}, Timestamp: s.now()})
}

func sampleProcess(pid, durationSec, intervalMS int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(durationSec+5)*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sample", fmt.Sprint(pid), fmt.Sprint(durationSec), fmt.Sprint(intervalMS)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sample pid %d: %w: %s", pid, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func callDaemon(network, address, method string, params interface{}) (interface{}, error) {
	conn, err := net.DialTimeout(network, address, 5*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if params == nil {
		params = map[string]interface{}{}
	}
	req := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	var resp struct {
		Result interface{} `json:"result"`
		Error  *RPCError   `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("remote error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func summarizeApp(r detect.Result) []string {
	out := []string{fmt.Sprintf("%s: UI=%s runtime=%s language=%s confidence=%s", r.Path, r.UI, r.Runtime, r.Language, r.Confidence)}
	if r.BundleID != "" || r.AppVersion != "" {
		out = append(out, fmt.Sprintf("bundle=%s version=%s build=%s", r.BundleID, r.AppVersion, r.BuildNumber))
	}
	if len(r.NativeModules) > 0 {
		out = append(out, fmt.Sprintf("native modules: %d", len(r.NativeModules)))
	}
	if r.Sandboxed || r.HardenedRuntime || len(r.Entitlements) > 0 {
		out = append(out, fmt.Sprintf("security: sandboxed=%t hardened_runtime=%t entitlements=%d", r.Sandboxed, r.HardenedRuntime, len(r.Entitlements)))
	}
	if r.Helpers != nil && (len(r.LoginItems) > 0 || len(r.Helpers.XPCServices) > 0 || len(r.Helpers.HelperApps) > 0) {
		out = append(out, fmt.Sprintf("helpers: login_items=%d xpc=%d helper_apps=%d", len(r.LoginItems), len(r.Helpers.XPCServices), len(r.Helpers.HelperApps)))
	}
	if len(r.NetworkEndpoints) > 0 {
		out = append(out, fmt.Sprintf("embedded network endpoints: %d", len(r.NetworkEndpoints)))
	}
	return out
}

func summarizeSnapshot(s snapshot.Snapshot, limit int) []string {
	if limit <= 0 {
		limit = 5
	}
	out := []string{
		fmt.Sprintf("host=%s os=%s %s apps=%d processes=%d jvms=%d", s.Host.Hostname, s.Host.OSName, s.Host.OSVersion, len(s.Apps), len(s.Processes), len(s.JVMs)),
		fmt.Sprintf("network: vpn=%t dns=%d listening_ports=%d", s.Network.VPNActive, len(s.Network.DNSServers), len(s.Network.ListeningPorts)),
		fmt.Sprintf("toolchains: jdks=%d brew_formulae=%d build_tools=%d", len(s.Toolchains.JDKs), len(s.Toolchains.Brew.Formulae), len(s.Toolchains.BuildTools)),
	}
	for i, app := range s.Apps {
		if i >= limit {
			break
		}
		out = append(out, fmt.Sprintf("app[%d]: %s %s/%s", i, app.Path, app.UI, app.Runtime))
	}
	return out
}

func summarizeProcesses(procs []process.Info, limit int) []string {
	if limit <= 0 || limit > len(procs) {
		limit = len(procs)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		p := procs[i]
		out = append(out, fmt.Sprintf("pid=%d ppid=%d cpu=%.1f rss=%dKiB threads=%d cmd=%s app=%s", p.PID, p.PPID, p.CPUPct, p.RSSKiB, p.ThreadCount, p.Command, p.AppPath))
	}
	return out
}

func summarizeJVM(info jvm.Info) []string {
	return []string{fmt.Sprintf("jvm pid=%d main=%s java=%s vendor=%s threads=%d jdk=%s", info.PID, info.MainClass, info.JDKVersion, info.JDKVendor, info.ThreadCount, info.JDKPath)}
}

func summarizeJVMs(infos []jvm.Info) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, summarizeJVM(info)...)
	}
	return out
}

func summarizeFindings(findings []rules.Finding, limit int) []string {
	if limit <= 0 || limit > len(findings) {
		limit = len(findings)
	}
	out := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		f := findings[i]
		subject := f.Subject
		if subject == "" {
			subject = "host"
		}
		out = append(out, fmt.Sprintf("%s %s [%s]: %s", f.Severity, f.RuleID, subject, f.Message))
	}
	return out
}

func triageSummary(evidence []string) string {
	if len(evidence) == 0 {
		return "no target-specific evidence collected"
	}
	return fmt.Sprintf("collected %d diagnostic signal(s)", len(evidence))
}

func filterAppProcesses(procs []process.Info, appPath string) []process.Info {
	var out []process.Info
	for _, p := range procs {
		if p.AppPath == appPath {
			out = append(out, p)
		}
	}
	sortProcesses(out)
	return out
}

func findProcess(procs []process.Info, pid int) (process.Info, bool) {
	for _, p := range procs {
		if p.PID == pid {
			return p, true
		}
	}
	return process.Info{}, false
}

func sortProcesses(procs []process.Info) {
	sort.SliceStable(procs, func(i, j int) bool {
		if procs[i].RSSKiB != procs[j].RSSKiB {
			return procs[i].RSSKiB > procs[j].RSSKiB
		}
		return procs[i].PID < procs[j].PID
	})
}

func summarizeNetworkState(s netstate.State) string {
	return fmt.Sprintf("network iface=%s vpn=%t dns=%d established=%d listening=%d", s.DefaultRouteIface, s.VPNActive, len(s.DNSServers), s.EstablishedConnectionsCount, len(s.ListeningPorts))
}

func networkEvidence(s netstate.State) []string {
	out := []string{fmt.Sprintf("default route: iface=%s gateway=%s", s.DefaultRouteIface, s.DefaultRouteGW)}
	if len(s.DNSServers) > 0 {
		out = append(out, "dns: "+strings.Join(s.DNSServers, ", "))
	}
	if s.Proxy.HTTP != "" || s.Proxy.HTTPS != "" || s.Proxy.SOCKS != "" {
		out = append(out, fmt.Sprintf("proxy: http=%s https=%s socks=%s", s.Proxy.HTTP, s.Proxy.HTTPS, s.Proxy.SOCKS))
	}
	if s.VPNActive {
		out = append(out, "vpn interfaces: "+strings.Join(s.VPNInterfaces, ", "))
	}
	return out
}

func connectionEvidenceForHost(conns []netstate.Connection, host string) []string {
	var out []string
	for _, c := range conns {
		if strings.Contains(c.RemoteAddr, host) || strings.Contains(c.LocalAddr, host) {
			out = append(out, fmt.Sprintf("connection pid=%d cmd=%s %s %s->%s state=%s", c.PID, c.Command, c.Proto, c.LocalAddr, c.RemoteAddr, c.State))
		}
	}
	if len(out) == 0 {
		out = append(out, "no active connection matched host "+host)
	}
	return out
}

func connectionEvidenceForPort(conns []netstate.Connection, port int) []string {
	needle := fmt.Sprintf(":%d", port)
	var out []string
	for _, c := range conns {
		if strings.Contains(c.RemoteAddr, needle) || strings.Contains(c.LocalAddr, needle) {
			out = append(out, fmt.Sprintf("connection pid=%d cmd=%s %s %s->%s state=%s", c.PID, c.Command, c.Proto, c.LocalAddr, c.RemoteAddr, c.State))
		}
	}
	if len(out) == 0 {
		out = append(out, fmt.Sprintf("no active connection matched port %d", port))
	}
	return out
}

func summarizeToolchains(tc toolchain.Toolchains) string {
	return fmt.Sprintf("toolchains jdks=%d node=%d python=%d go=%d ruby=%d rust=%d build_tools=%d", len(tc.JDKs), len(tc.Node), len(tc.Python), len(tc.Go), len(tc.Ruby), len(tc.Rust), len(tc.BuildTools))
}

func toolchainDriftEvidence(tc toolchain.Toolchains) []string {
	var out []string
	byMajor := map[int]int{}
	for _, jdk := range tc.JDKs {
		byMajor[jdk.VersionMajor]++
	}
	for major, count := range byMajor {
		if count > 1 {
			out = append(out, fmt.Sprintf("multiple JDK %d installs: %d", major, count))
		}
	}
	if tc.Env.JavaHome != "" {
		matched := false
		for _, jdk := range tc.JDKs {
			if strings.HasPrefix(tc.Env.JavaHome, jdk.Path) || strings.HasPrefix(jdk.Path, tc.Env.JavaHome) {
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, "JAVA_HOME does not match a discovered JDK: "+tc.Env.JavaHome)
		}
	}
	return out
}
