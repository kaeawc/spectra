package jvm

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/kaeawc/spectra/internal/recording"
)

func TestParseJFRViewTable(t *testing.T) {
	out := `
Longest GC Pauses

Start Time                 Duration  GC Cause
------------------------------------------------
21:00:01.123               45.1 ms   Allocation Failure
21:00:15.002               12.3 ms   Metadata GC Threshold
`
	got := ParseJFRView("/tmp/test.jfr", JFRViewGCPauses, out)
	if got.Path != "/tmp/test.jfr" || got.View != JFRViewGCPauses {
		t.Fatalf("identity = (%q, %q)", got.Path, got.View)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("tables = %d, want 1: %+v", len(got.Tables), got.Tables)
	}
	table := got.Tables[0]
	if table.Title != "Longest GC Pauses" {
		t.Fatalf("title = %q", table.Title)
	}
	wantColumns := []string{"Start Time", "Duration", "GC Cause"}
	if !reflect.DeepEqual(table.Columns, wantColumns) {
		t.Fatalf("columns = %#v, want %#v", table.Columns, wantColumns)
	}
	if len(table.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(table.Rows))
	}
	if table.Rows[0]["Duration"] != "45.1 ms" || table.Rows[0]["GC Cause"] != "Allocation Failure" {
		t.Fatalf("row[0] = %+v", table.Rows[0])
	}
}

func TestViewJFRFakeRunner(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("Method  Samples\n---------------\nmain    10\n"), nil
	}
	got, err := ViewJFR("/tmp/test.jfr", JFRViewHotMethods, run)
	if err != nil {
		t.Fatalf("ViewJFR: %v", err)
	}
	wantArgs := []string{"view", JFRViewHotMethods, "/tmp/test.jfr"}
	if gotName != "jfr" || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command = %s %v, want jfr %v", gotName, gotArgs, wantArgs)
	}
	if len(got.Tables) != 1 || got.Tables[0].Rows[0]["Method"] != "main" {
		t.Fatalf("tables = %+v", got.Tables)
	}
}

func TestAnalyzeJFRCollectsViewsAndNarrative(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name != "jfr" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		switch args[0] {
		case "summary":
			return []byte(`Version: 2.1
jdk.GarbageCollection 2 64
jdk.ObjectAllocationInNewTLAB 50 800
`), nil
		case "view":
			switch args[1] {
			case JFRViewGCPauses:
				return []byte("Start Time  Duration\n--------------------\n21:00:01    45 ms\n"), nil
			case JFRViewAllocationSite:
				return []byte("Class  Bytes\n------------\nbyte[]  1024\n"), nil
			}
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	got, err := AnalyzeJFR("/tmp/test.jfr", JFRAnalysisOptions{Views: []string{JFRViewGCPauses, JFRViewAllocationSite}}, run)
	if err != nil {
		t.Fatalf("AnalyzeJFR: %v", err)
	}
	if len(got.Views) != 2 {
		t.Fatalf("views = %d, want 2", len(got.Views))
	}
	if got.Artifact.Kind != "jfr" || got.Artifact.Runtime != "jvm" || got.Artifact.Architecture != "java" {
		t.Fatalf("artifact = %+v", got.Artifact)
	}
	if len(got.Narrative) < 4 {
		t.Fatalf("narrative = %+v, want summary and view findings", got.Narrative)
	}
}

func TestJFRAnalyzerImplementsRecordingAnalyzer(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "summary":
			return []byte("Version: 2.1\n"), nil
		case "view":
			return []byte("Method  Samples\n---------------\nmain    10\n"), nil
		}
		return nil, fmt.Errorf("unexpected args %v", args)
	}
	var analyzer recording.Analyzer = JFRAnalyzer{Run: run}
	got, err := analyzer.AnalyzeRecording("/tmp/test.jfr", recording.Options{Views: []string{JFRViewHotMethods}})
	if err != nil {
		t.Fatalf("AnalyzeRecording: %v", err)
	}
	if got.Artifact.Kind != "jfr" || len(got.Views) != 1 || got.Views[0].View != JFRViewHotMethods {
		t.Fatalf("analysis = %+v", got)
	}
}

func TestAnalyzeJFRPropagatesViewError(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if args[0] == "summary" {
			return []byte("Version: 2.1\n"), nil
		}
		return nil, fmt.Errorf("view failed")
	}
	if _, err := AnalyzeJFR("/tmp/test.jfr", JFRAnalysisOptions{Views: []string{JFRViewGCPauses}}, run); err == nil {
		t.Fatal("expected error")
	}
}
