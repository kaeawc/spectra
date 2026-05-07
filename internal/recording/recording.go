// Package recording defines reusable analysis contracts for diagnostic
// recordings captured from applications and their supporting runtimes.
package recording

import "fmt"

// Artifact describes a recording without assuming a specific runtime.
type Artifact struct {
	Path         string `json:"path,omitempty"`
	Kind         string `json:"kind,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	Architecture string `json:"architecture,omitempty"`
}

// Options carries analyzer-independent knobs for selecting event views.
type Options struct {
	Views []string `json:"views,omitempty"`
}

// Analyzer turns one diagnostic recording into structured views and findings.
type Analyzer interface {
	AnalyzeRecording(path string, opts Options) (Analysis, error)
}

// AnalyzerFunc adapts a function into an Analyzer.
type AnalyzerFunc func(path string, opts Options) (Analysis, error)

// AnalyzeRecording implements Analyzer.
func (f AnalyzerFunc) AnalyzeRecording(path string, opts Options) (Analysis, error) {
	return f(path, opts)
}

// Registry dispatches recording analysis by artifact kind.
type Registry struct {
	analyzers map[string]Analyzer
}

// NewRegistry returns an empty analyzer registry.
func NewRegistry() *Registry {
	return &Registry{analyzers: make(map[string]Analyzer)}
}

// Register binds an analyzer to a recording kind.
func (r *Registry) Register(kind string, analyzer Analyzer) {
	if r.analyzers == nil {
		r.analyzers = make(map[string]Analyzer)
	}
	r.analyzers[kind] = analyzer
}

// Analyze finds the analyzer for kind and runs it.
func (r *Registry) Analyze(kind, path string, opts Options) (Analysis, error) {
	if r == nil {
		return Analysis{}, fmt.Errorf("recording analyzer registry is nil")
	}
	analyzer, ok := r.analyzers[kind]
	if !ok || analyzer == nil {
		return Analysis{}, fmt.Errorf("no recording analyzer registered for %q", kind)
	}
	return analyzer.AnalyzeRecording(path, opts)
}

// Analysis combines runtime-specific metadata, parsed event views, and findings.
type Analysis struct {
	Artifact  Artifact  `json:"artifact"`
	Summary   any       `json:"summary,omitempty"`
	Views     []View    `json:"views,omitempty"`
	Narrative []Finding `json:"narrative,omitempty"`
}

// View is one named perspective over a recording's event stream.
type View struct {
	Path   string  `json:"path,omitempty"`
	View   string  `json:"view"`
	Tables []Table `json:"tables,omitempty"`
	Raw    string  `json:"raw,omitempty"`
}

// Table is one tabular section emitted by an analyzer backend.
type Table struct {
	Title   string   `json:"title,omitempty"`
	Columns []string `json:"columns,omitempty"`
	Rows    []Row    `json:"rows,omitempty"`
}

// Row is one structured event-view row.
type Row map[string]string

// Finding is a human-oriented observation derived from a recording.
type Finding struct {
	Area     string `json:"area"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
}
