package recording

import "testing"

func TestRegistryDispatchesAnalyzerByKind(t *testing.T) {
	registry := NewRegistry()
	registry.Register("sample", AnalyzerFunc(func(path string, opts Options) (Analysis, error) {
		return Analysis{
			Artifact: Artifact{Path: path, Kind: "sample", Runtime: "native"},
			Views:    []View{{View: opts.Views[0]}},
		}, nil
	}))

	got, err := registry.Analyze("sample", "/tmp/app.trace", Options{Views: []string{"latency"}})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got.Artifact.Kind != "sample" || got.Artifact.Runtime != "native" {
		t.Fatalf("artifact = %+v", got.Artifact)
	}
	if len(got.Views) != 1 || got.Views[0].View != "latency" {
		t.Fatalf("views = %+v", got.Views)
	}
}

func TestRegistryReportsMissingAnalyzer(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Analyze("missing", "/tmp/app.trace", Options{}); err == nil {
		t.Fatal("expected error")
	}
}
