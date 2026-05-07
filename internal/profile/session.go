package profile

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Architecture describes the kind of application/runtime being profiled.
type Architecture string

const (
	ArchitectureUnknown Architecture = "unknown"
	ArchitectureJVM     Architecture = "jvm"
	ArchitectureNative  Architecture = "native"
	ArchitectureNode    Architecture = "node"
	ArchitecturePython  Architecture = "python"
	ArchitectureBrowser Architecture = "browser"
)

// Workflow describes the profiling mechanism used by a session.
type Workflow string

const (
	WorkflowSampling        Workflow = "sampling"
	WorkflowInstrumentation Workflow = "instrumentation"
)

// View names an analysis view that can be built from captured profile data.
type View string

const (
	ViewCallTree           View = "call_tree"
	ViewHotMethods         View = "hot_methods"
	ViewAllocationTimeline View = "allocation_timeline"
	ViewLockContention     View = "lock_contention"
)

// Target describes the application or runtime a profiler session attaches to.
// PID is common today, but BundleID, Executable, and RuntimeID let future
// collectors target app bundles, helper processes, browser renderers, or
// language runtime identifiers without changing the session contract.
type Target struct {
	PID          int               `json:"pid,omitempty"`
	Architecture Architecture      `json:"architecture,omitempty"`
	BundleID     string            `json:"bundle_id,omitempty"`
	Executable   string            `json:"executable,omitempty"`
	RuntimeID    string            `json:"runtime_id,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// SessionSpec is the reusable planning shape for profiler captures. Frontends
// can build it before choosing a concrete collector.
type SessionSpec struct {
	Target Target `json:"target"`
	// TargetPID keeps existing callers source-compatible while they migrate to
	// Target. New application architectures should populate Target directly.
	TargetPID int           `json:"target_pid"`
	Collector string        `json:"collector"`
	Event     string        `json:"event"`
	Workflow  Workflow      `json:"workflow"`
	Duration  time.Duration `json:"duration"`
	Views     []View        `json:"views,omitempty"`
}

// Artifact is the normalized output from a collector. The bytes may live in a
// file path, cache key, or inline stream depending on the caller's storage layer.
type Artifact struct {
	Kind     string            `json:"kind"`
	Path     string            `json:"path,omitempty"`
	MimeType string            `json:"mime_type,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Collector is implemented by runtime-specific capture adapters such as JFR,
// async-profiler, sample, DTrace, or future Node/Python collectors.
type Collector interface {
	Name() string
	Supports(Target) bool
	Capture(context.Context, SessionSpec) (Artifact, error)
}

// Analyzer turns normalized samples or artifacts into reusable analysis views.
type Analyzer interface {
	Views() []View
	Analyze(context.Context, Artifact) (Analysis, error)
}

// Analysis is a collector-independent profile view container.
type Analysis struct {
	Target     Target                 `json:"target,omitempty"`
	Event      string                 `json:"event,omitempty"`
	Views      []View                 `json:"views,omitempty"`
	CallTree   *CallTreeNode          `json:"call_tree,omitempty"`
	HotMethods []MethodStat           `json:"hot_methods,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// NormalizeSession applies Spectra's profiling defaults and validates fields
// that are independent of a specific transport or command frontend.
func NormalizeSession(spec SessionSpec) (SessionSpec, error) {
	spec.Target = normalizeTarget(spec.Target, spec.TargetPID)
	spec.Collector = normalizeCollector(spec.Collector, spec.Target.Architecture)
	spec.Event = normalizeEvent(spec.Event)
	spec.Workflow = normalizeWorkflow(spec.Workflow, spec.Collector, spec.Event)
	spec.Duration = normalizeDuration(spec.Duration)
	if spec.Workflow != WorkflowSampling && spec.Workflow != WorkflowInstrumentation {
		return SessionSpec{}, fmt.Errorf("unknown profiling workflow %q", spec.Workflow)
	}
	if spec.Duration < 0 {
		return SessionSpec{}, fmt.Errorf("duration must be greater than zero")
	}
	if !hasTarget(spec.Target) {
		return SessionSpec{}, fmt.Errorf("profile target is required")
	}
	if len(spec.Views) == 0 {
		spec.Views = DefaultViews(spec.Event)
	}
	spec.TargetPID = spec.Target.PID
	return spec, nil
}

func normalizeTarget(target Target, fallbackPID int) Target {
	if target.PID == 0 && fallbackPID != 0 {
		target.PID = fallbackPID
	}
	if target.Architecture == "" {
		target.Architecture = ArchitectureUnknown
	}
	return target
}

func hasTarget(target Target) bool {
	return target.PID > 0 || target.BundleID != "" || target.Executable != "" || target.RuntimeID != ""
}

func normalizeCollector(collector string, architecture Architecture) string {
	collector = strings.TrimSpace(collector)
	if collector == "" {
		return DefaultCollector(architecture)
	}
	return collector
}

func normalizeEvent(event string) string {
	event = strings.TrimSpace(event)
	if event == "" {
		return "cpu"
	}
	return event
}

func normalizeWorkflow(workflow Workflow, collector, event string) Workflow {
	if workflow == "" {
		return defaultWorkflow(collector, event)
	}
	return workflow
}

func normalizeDuration(duration time.Duration) time.Duration {
	if duration == 0 {
		return 30 * time.Second
	}
	return duration
}

// DefaultCollector returns a conservative first-choice collector by architecture.
func DefaultCollector(architecture Architecture) string {
	switch architecture {
	case ArchitectureJVM:
		return "async-profiler"
	case ArchitectureNative:
		return "sample"
	case ArchitectureNode:
		return "node-profiler"
	case ArchitecturePython:
		return "py-spy"
	case ArchitectureBrowser:
		return "browser-profiler"
	default:
		return "process-sampler"
	}
}

// DefaultViews returns analysis views usually useful for a profiler event.
func DefaultViews(event string) []View {
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "alloc", "allocation", "objectallocationinsample", "jdk.objectallocationinsample":
		return []View{ViewHotMethods, ViewCallTree, ViewAllocationTimeline}
	case "lock", "monitor-blocked", "jdk.javamonitorenter":
		return []View{ViewLockContention, ViewHotMethods, ViewCallTree}
	default:
		return []View{ViewHotMethods, ViewCallTree}
	}
}

func defaultWorkflow(collector, event string) Workflow {
	switch strings.ToLower(strings.TrimSpace(collector)) {
	case "jfr":
		return WorkflowInstrumentation
	}
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "cpu", "wall", "alloc", "lock":
		return WorkflowSampling
	default:
		return WorkflowInstrumentation
	}
}
