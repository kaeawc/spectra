package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/gclog"
)

func TestGCLogSummaryRenders(t *testing.T) {
	log := "[0.1s][info][gc] GC(0) Pause Young (G1 Evacuation Pause) 25M->5M(256M) 4.0ms\n" +
		"[1.0s][info][gc] GC(1) Pause Full (System.gc()) 100M->40M(256M) 45.0ms\n"
	p := filepath.Join(t.TempDir(), "gc.log")
	if err := os.WriteFile(p, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := gclog.ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Pauses != 2 || summary.FullGCCount != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	var buf bytes.Buffer
	printGCLogSummary(&buf, p, summary)
	out := buf.String()
	if !strings.Contains(out, "2 pauses") || !strings.Contains(out, "Longest: GC(1) Pause Full") {
		t.Fatalf("render:\n%s", out)
	}
	if !strings.Contains(out, "Causes:") {
		t.Fatalf("missing causes section:\n%s", out)
	}
}
