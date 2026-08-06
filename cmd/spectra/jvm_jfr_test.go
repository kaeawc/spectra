package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

const sampleJFRView = `
Longest GC Pauses

Start Time                 Duration  GC Cause
------------------------------------------------
21:00:01.123               45.1 ms   Allocation Failure
21:00:15.002               12.3 ms   Metadata GC Threshold
`

func TestPrintJFRViewRendersTable(t *testing.T) {
	view := jvm.ParseJFRView("/tmp/rec.jfr", jvm.JFRViewGCPauses, sampleJFRView)
	if len(view.Tables) == 0 {
		t.Fatalf("expected a parsed table")
	}
	var buf bytes.Buffer
	printJFRView(&buf, view)
	out := buf.String()
	if !strings.Contains(out, "Longest GC Pauses") {
		t.Fatalf("missing table title:\n%s", out)
	}
	if !strings.Contains(out, "Allocation Failure") {
		t.Fatalf("missing row data:\n%s", out)
	}
}

func TestPrintJFRAnalysisRendersFindings(t *testing.T) {
	a := jvm.JFRAnalysis{}
	a.Artifact.Path = "/tmp/rec.jfr"
	a.Narrative = []jvm.JFRFinding{
		{Area: "gc", Severity: "warn", Summary: "long GC pause observed", Detail: "45.1 ms allocation failure"},
	}
	a.Views = []jvm.JFRViewResult{{View: jvm.JFRViewGCPauses}}

	var buf bytes.Buffer
	printJFRAnalysis(&buf, a)
	out := buf.String()
	if !strings.Contains(out, "JFR analysis: /tmp/rec.jfr") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "long GC pause observed") {
		t.Fatalf("missing finding:\n%s", out)
	}
}

func TestPrintJFRAnalysisNoFindings(t *testing.T) {
	var buf bytes.Buffer
	printJFRAnalysis(&buf, jvm.JFRAnalysis{})
	if !strings.Contains(buf.String(), "No incident findings.") {
		t.Fatalf("expected empty-findings message, got:\n%s", buf.String())
	}
}
