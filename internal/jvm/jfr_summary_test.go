package jvm

import (
	"fmt"
	"testing"
)

func TestParseJFRSummary(t *testing.T) {
	out := `Version: 2.1
Chunks: 1
Start: 2026-05-05 21:00:00 (UTC)
Duration: 60 s

Event Type                                   Count  Size (bytes)
---------------------------------------------------------------
jdk.CPULoad                                     60        1,920
jdk.ExecutionSample                          1,234       98,765
`
	got := ParseJFRSummary("/tmp/test.jfr", out)
	if got.Path != "/tmp/test.jfr" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.Version != "2.1" {
		t.Errorf("Version = %q", got.Version)
	}
	if got.Chunks != 1 {
		t.Errorf("Chunks = %d", got.Chunks)
	}
	if got.Duration != "60 s" {
		t.Errorf("Duration = %q", got.Duration)
	}
	if len(got.Events) != 2 {
		t.Fatalf("Events = %d, want 2: %+v", len(got.Events), got.Events)
	}
	if got.Events[1].Type != "jdk.ExecutionSample" || got.Events[1].Count != 1234 || got.Events[1].SizeBytes != 98765 {
		t.Errorf("event[1] = %+v", got.Events[1])
	}
}

func TestSummarizeJFRFakeRunner(t *testing.T) {
	var gotName string
	var gotArgs []string
	run := func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("Version: 2.1\nChunks: 1\njdk.CPULoad 3 96\n"), nil
	}
	summary, err := SummarizeJFR("/tmp/test.jfr", run)
	if err != nil {
		t.Fatalf("SummarizeJFR: %v", err)
	}
	if gotName != "jfr" || len(gotArgs) != 2 || gotArgs[0] != "summary" || gotArgs[1] != "/tmp/test.jfr" {
		t.Fatalf("command = %s %v", gotName, gotArgs)
	}
	if len(summary.Events) != 1 || summary.Events[0].Count != 3 {
		t.Fatalf("summary events = %+v", summary.Events)
	}
}

func TestSummarizeJFRPropagatesRunnerError(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("no jfr")
	}
	if _, err := SummarizeJFR("/tmp/test.jfr", run); err == nil {
		t.Fatal("expected error")
	}
}
