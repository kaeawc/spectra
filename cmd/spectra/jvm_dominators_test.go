package main

import (
	"os"
	"path/filepath"
	"testing"
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
