package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/spawntrace"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// jobSpec is one tool job_start may wrap. validate checks the arguments as
// the tool itself does, so a bad call fails at job_start rather than in the
// background; it returns the operation the job runs ("" for single-purpose
// tools).
type jobSpec struct {
	validate func(json.RawMessage) (string, error)
}

// jobTools is the allowlist of slow tools job_start wraps. process, jvm and
// snapshot are wrapped only for their slow operations.
var jobTools = map[string]jobSpec{
	"spawns":   {validate: validateSpawnsArgs},
	"idle":     {validate: validateIdleArgs},
	"load":     {validate: validateLoadArgs},
	"orphans":  {validate: validateOrphansArgs},
	"triage":   {validate: validateTriageArgs},
	"diagnose": {validate: validateDiagnoseArgs},
	"snapshot": {validate: validateSnapshotArgs},
	"process":  {validate: validateProcessArgs},
	"jvm":      {validate: validateJVMArgs},
}

func jobToolNames() []string {
	names := make([]string, 0, len(jobTools))
	for name := range jobTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// jobToolReadOnly is each job tool's readOnlyHint. job_start can wrap
// state-changing tools (snapshot store, diagnose persist, triage samples)
// and job_cancel stops a job, so neither is read-only.
var jobToolReadOnly = map[string]bool{
	"job_start":  false,
	"job_status": true,
	"job_result": true,
	"job_cancel": false,
	"job_list":   true,
}

func jobToolDefinitions() []ToolDefinition {
	idSchema := objectSchema(map[string]interface{}{"job_id": stringProp("Job ID from job_start or job_list.")}, []string{"job_id"})
	return []ToolDefinition{
		{
			Name: "job_start",
			Description: "Run a slow tool in the background and return its job record (as job_status) at once. process only for operation=sample, jvm only for flamegraph, snapshot only for create or baseline; arguments are checked now. " +
				"Poll job_status until completed, failed or cancelled, then fetch the result with job_result. Ask: \"Trace spawns for 30s without blocking.\"",
			InputSchema: objectSchema(map[string]interface{}{
				"tool":      enumProp(jobToolNames(), "spawns"),
				"arguments": map[string]interface{}{"type": "object", "description": "The wrapped tool's arguments, as for a direct call."},
			}, []string{"tool"}),
		},
		{Name: "job_status", Description: "A job's record, with the latest progress its tool reported; ttl and pollInterval are in ms.", InputSchema: idSchema},
		{Name: "job_result", Description: "The wrapped tool's result, exactly as a direct call returns it, once the job is completed or failed. Errors while working.", InputSchema: idSchema},
		{Name: "job_cancel", Description: "Cancel a working job: it becomes cancelled at once, its tool is stopped, and its record is returned.", InputSchema: idSchema},
		{Name: "job_list", Description: "This session's jobs, oldest first, including finished ones until they expire.", InputSchema: objectSchema(map[string]interface{}{}, nil)},
	}
}

// jobOutputSchema spells out the job record once, on job_status; job_start
// and job_cancel return the same record and say so in their descriptions.
func jobOutputSchema(name string) *jsonschema.Schema {
	switch name {
	case "job_status":
		return jobRecordSchema()
	case "job_list":
		s := schemaForType(reflect.TypeFor[jobList]())
		if items := s.Properties["jobs"]; items != nil && items.Items != nil {
			constrainJobStatus(items.Items)
		}
		return s
	default:
		return permissiveObjectSchema()
	}
}

func jobRecordSchema() *jsonschema.Schema {
	s := schemaForType(reflect.TypeFor[jobRecord]())
	constrainJobStatus(s)
	return s
}

func constrainJobStatus(s *jsonschema.Schema) {
	if st := s.Properties["status"]; st != nil {
		for _, v := range jobStatuses {
			st.Enum = append(st.Enum, string(v))
		}
	}
}

func jobSDKTool(def ToolDefinition) *sdk.Tool {
	t := &sdk.Tool{Name: def.Name, Description: def.Description, InputSchema: def.InputSchema, OutputSchema: jobOutputSchema(def.Name)}
	if jobToolReadOnly[def.Name] {
		t.Annotations = &sdk.ToolAnnotations{ReadOnlyHint: true}
	}
	return t
}

// jobToolHandler serves a job tool. Job tools answer from the store without
// the call slot, so they work while a direct call or a job runs.
func (s *Server) jobToolHandler(name string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var owner any
		var args json.RawMessage
		if req != nil {
			owner = req.Session
			if req.Params != nil {
				args = req.Params.Arguments
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res := s.jobTool(ctx, name, owner, args)
		// A started or cancelled job is a state change the caller must learn
		// about (the job id, the cancelled record), so only reads give way to a
		// request cancelled meanwhile.
		if err := ctx.Err(); err != nil && jobToolReadOnly[name] {
			return nil, err
		}
		return sdkResult(res), nil
	}
}

// jobTool runs one job tool for owner (the calling session).
func (s *Server) jobTool(ctx context.Context, name string, owner any, raw json.RawMessage) ToolResult {
	if s.jobs == nil {
		return toolError("jobs are not available")
	}
	if owner == nil {
		return toolError("jobs need a session")
	}
	if name == "job_start" {
		return s.jobStart(ctx, owner, raw)
	}
	if name == "job_list" {
		return toolText(s.jobs.list(owner))
	}
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	if p.JobID == "" {
		return toolError("job_id is required")
	}
	switch name {
	case "job_status":
		return recordResult(s.jobs.status(owner, p.JobID))
	case "job_cancel":
		return recordResult(s.jobs.cancelJob(owner, p.JobID))
	case "job_result":
		res, err := s.jobs.result(owner, p.JobID)
		if err != nil {
			return toolErrorFrom(err)
		}
		return res
	default:
		return toolError("unknown job tool: " + name)
	}
}

func recordResult(r jobRecord, err error) ToolResult {
	if err != nil {
		return toolErrorFrom(err)
	}
	return toolText(r)
}

func (s *Server) jobStart(ctx context.Context, owner any, raw json.RawMessage) ToolResult {
	var p struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return toolErrorFrom(err)
	}
	spec, ok := jobTools[p.Tool]
	if !ok {
		return toolError(fmt.Sprintf("job_start: tool must be one of %s, got %q", strings.Join(jobToolNames(), ", "), p.Tool))
	}
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" && !strings.HasPrefix(strings.TrimSpace(string(p.Arguments)), "{") {
		return toolError("job_start: arguments must be an object")
	}
	op, err := spec.validate(p.Arguments)
	if err != nil {
		return toolErrorFrom(err)
	}
	handler := s.toolHandlers()[p.Tool]
	rec, err := s.jobs.startCtx(ctx, owner, p.Tool, op, p.Arguments, handler)
	if err != nil {
		return toolError("job_start: " + err.Error())
	}
	return toolText(rec)
}

// The validators below repeat each wrapped handler's argument checks (and
// error text) without running the tool; TestJobValidationMatchesHandlers
// keeps them in step.

func validateSpawnsArgs(raw json.RawMessage) (string, error) {
	var p struct {
		DurationSeconds *int `json:"duration_seconds"`
		IntervalMillis  *int `json:"interval_millis"`
		PID             int  `json:"pid"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	opts := spawntrace.DefaultOptions()
	opts.PID = p.PID
	opts.Duration = defaultSpawnsSeconds * time.Second
	if p.DurationSeconds != nil {
		if *p.DurationSeconds < 1 || *p.DurationSeconds > spawnsMaxSeconds {
			return "", fmt.Errorf("duration_seconds must be 1..%d", spawnsMaxSeconds)
		}
		opts.Duration = time.Duration(*p.DurationSeconds) * time.Second
	}
	if p.IntervalMillis != nil {
		if *p.IntervalMillis < spawnsMinIntervalMS {
			return "", fmt.Errorf("interval_millis must be at least %d", spawnsMinIntervalMS)
		}
		opts.Interval = time.Duration(*p.IntervalMillis) * time.Millisecond
	}
	if err := opts.Validate(); err != nil {
		return "", errors.New("spawns: " + err.Error())
	}
	return "", nil
}

func validateIdleArgs(raw json.RawMessage) (string, error) {
	var p struct {
		Roots  []string `json:"roots"`
		PIDs   []int    `json:"pids"`
		MinAge string   `json:"min_age"`
		MaxCPU *float64 `json:"max_cpu"`
		Window string   `json:"window"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	_, err := idleOptions(p.Roots, p.PIDs, p.MinAge, p.Window, p.MaxCPU)
	return "", err
}

func validateLoadArgs(raw json.RawMessage) (string, error) {
	var p struct {
		Roots         []string `json:"roots"`
		MaxLoadPerCPU *float64 `json:"max_load_per_cpu"`
		MinFreeGB     *float64 `json:"min_free_gb"`
		BackoffFreeGB *float64 `json:"backoff_free_gb"`
		Top           *int     `json:"top"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	opts := hostload.DefaultReportOptions()
	opts.Roots = p.Roots
	setIf(&opts.MaxLoadPerCPU, p.MaxLoadPerCPU)
	setIf(&opts.MinFreeGB, p.MinFreeGB)
	setIf(&opts.BackoffFreeGB, p.BackoffFreeGB)
	setIf(&opts.Top, p.Top)
	if err := opts.Validate(); err != nil {
		return "", err
	}
	if opts.Top > loadMaxTop {
		return "", fmt.Errorf("top must be 1..%d", loadMaxTop)
	}
	return "", absRoots("roots", opts.Roots)
}

func validateOrphansArgs(raw json.RawMessage) (string, error) {
	var p struct {
		Roots  []string `json:"roots"`
		MinAge string   `json:"min_age"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	if len(p.Roots) == 0 {
		return "", errors.New("roots is required")
	}
	if err := absRoots("roots", p.Roots); err != nil {
		return "", err
	}
	minAge, err := durationArg("min_age", p.MinAge, orphans.DefaultMinAge)
	if err != nil {
		return "", err
	}
	if minAge < 0 {
		return "", errors.New("min_age must not be negative")
	}
	return "", nil
}

func validateTriageArgs(raw json.RawMessage) (string, error) {
	var p triageParams
	return "", decodeArgs(raw, &p)
}

func validateDiagnoseArgs(raw json.RawMessage) (string, error) {
	var p struct {
		SnapshotID     string `json:"snapshot_id"`
		RulesConfig    string `json:"rules_config"`
		Persist        bool   `json:"persist"`
		IncludeRaw     bool   `json:"include_raw"`
		ThreadDumpPIDs []int  `json:"thread_dump_pids"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	if p.SnapshotID != "" && len(p.ThreadDumpPIDs) > 0 {
		return "", errors.New("thread_dump_pids requires a live snapshot; omit snapshot_id")
	}
	return "", nil
}

func validateSnapshotArgs(raw json.RawMessage) (string, error) {
	var p snapshotParams
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	if p.Operation == "" {
		p.Operation = "create"
	}
	if p.Operation != "create" && p.Operation != "baseline" {
		return "", fmt.Errorf("job_start wraps snapshot only for operation create or baseline, not %q", p.Operation)
	}
	return p.Operation, nil
}

func validateProcessArgs(raw json.RawMessage) (string, error) {
	var p struct {
		Operation       string `json:"operation"`
		PID             int    `json:"pid"`
		DurationSeconds int    `json:"duration_seconds"`
		IntervalMillis  int    `json:"interval_millis"`
	}
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	if p.Operation != "sample" {
		return "", fmt.Errorf("job_start wraps process only for operation sample, not %q", p.Operation)
	}
	if p.PID == 0 {
		return "", errors.New("process sample requires pid")
	}
	if p.DurationSeconds < 0 || p.DurationSeconds > maxCaptureSeconds {
		return "", fmt.Errorf("duration_seconds must be 1..%d", maxCaptureSeconds)
	}
	return p.Operation, nil
}

func validateJVMArgs(raw json.RawMessage) (string, error) {
	var p jvmParams
	if err := decodeArgs(raw, &p); err != nil {
		return "", err
	}
	if p.Operation != "flamegraph" {
		return "", fmt.Errorf("job_start wraps jvm only for operation flamegraph, not %q", p.Operation)
	}
	return p.Operation, checkFlamegraphArgs(p)
}
