package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
)

func TestScanInspectCountsFailures(t *testing.T) {
	dir := t.TempDir()
	notApp := filepath.Join(dir, "plain-dir")
	if err := os.Mkdir(notApp, 0o755); err != nil {
		t.Fatal(err)
	}
	results, failed := scanInspect([]string{filepath.Join(dir, "Missing.app"), notApp}, detect.Options{})
	if len(results) != 0 || failed != 2 {
		t.Fatalf("scanInspect = %d results, %d failed; want 0, 2", len(results), failed)
	}
}

func TestRunInspectExitsNonZeroOnBadPath(t *testing.T) {
	if hostos.Current() != hostos.Darwin {
		t.Skip("inspect is macOS-only")
	}
	missing := filepath.Join(t.TempDir(), "Missing.app")
	for _, args := range [][]string{{missing}, {"--json", missing}} {
		var got int
		captureStdout(t, func() { got = runInspect(args) })
		if got != 1 {
			t.Errorf("runInspect(%q) = %d, want 1", args, got)
		}
	}
}
