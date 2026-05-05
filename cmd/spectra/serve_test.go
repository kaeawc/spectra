package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenDaemonLoggerDisabled(t *testing.T) {
	log, closeFn, path, err := openDaemonLogger("", true)
	if err != nil {
		t.Fatal(err)
	}
	if log != nil || closeFn != nil || path != "" {
		t.Fatalf("disabled logger = (%v, closeFn nil %t, %q), want nil true empty", log, closeFn == nil, path)
	}
}

func TestOpenDaemonLoggerCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.jsonl")
	log, closeFn, gotPath, err := openDaemonLogger(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if closeFn == nil {
		t.Fatal("closeFn is nil")
	}
	defer closeFn()
	if log == nil {
		t.Fatal("log is nil")
	}
	if gotPath != path {
		t.Fatalf("path = %q, want %q", gotPath, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
