package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/heap"
)

func TestRunJVMDominatorsArgValidation(t *testing.T) {
	for _, args := range [][]string{
		{},                     // no file
		{"a.hprof", "b.hprof"}, // too many
		{"--top", "-1", "a.hprof"},
	} {
		if code := runJVMDominators(args); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunJVMDominatorsBadFile(t *testing.T) {
	if code := runJVMDominators([]string{filepath.Join(t.TempDir(), "missing.hprof")}); code != 1 {
		t.Errorf("missing file: exit = %d, want 1", code)
	}
}

func TestRunJVMDominatorsInvalidHPROF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.hprof")
	if err := os.WriteFile(path, []byte("not an hprof file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runJVMDominators([]string{path}); code != 1 {
		t.Errorf("invalid hprof: exit = %d, want 1", code)
	}
}

func TestRunJVMHeapHPROFRetained(t *testing.T) {
	path := writeHPROF(t, "dump.hprof", 3)
	if code := runJVMHeapHPROF([]string{"--retained", "--suspects", "5", path}); code != 0 {
		t.Fatalf("heap-hprof --retained exit = %d, want 0", code)
	}
	if code := runJVMHeapHPROF([]string{"--retained", "--json", path}); code != 0 {
		t.Fatalf("heap-hprof --retained --json exit = %d, want 0", code)
	}
	if code := runJVMHeapHPROF([]string{"--retained", filepath.Join(t.TempDir(), "missing.hprof")}); code != 1 {
		t.Fatalf("missing file exit = %d, want 1", code)
	}
}

func TestPrintDominatorsIncludesClasses(t *testing.T) {
	var buf bytes.Buffer
	printDominators(&buf, "x.hprof", heap.DominatorResult{
		ReachableObjects: 1,
		ReachableBytes:   24,
		Suspects:         []heap.RetainedSuspect{{ID: 0x10, ClassName: "com.acme.Widget", RetainedBytes: 24, PercentOfHeap: 100}},
		Classes:          []heap.ClassRetained{{ClassName: "com.acme.Widget", Instances: 1, ShallowBytes: 24, RetainedBytes: 24, PercentOfHeap: 100}},
	})
	out := buf.String()
	for _, want := range []string{"Top classes by retained size", "Top objects by retained size", "com.acme.Widget (0x10)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
