package main

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestServeChildArgsDropsDaemonFlag(t *testing.T) {
	got := serveChildArgs([]string{"--daemon", "--sock", "/tmp/spectra.sock", "--no-log-file"})
	want := []string{"serve", "--sock", "/tmp/spectra.sock", "--no-log-file"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serveChildArgs = %v, want %v", got, want)
	}
}

func TestRunServeDaemonStartsDetached(t *testing.T) {
	old := startDetachedServeFunc
	t.Cleanup(func() { startDetachedServeFunc = old })
	var got []string
	startDetachedServeFunc = func(args []string) error {
		got = append([]string(nil), args...)
		return nil
	}

	code := runServe([]string{"--daemon", "--sock", "/tmp/spectra.sock", "--no-log-file"})
	if code != 0 {
		t.Fatalf("runServe code = %d, want 0", code)
	}
	want := []string{"serve", "--sock", "/tmp/spectra.sock", "--no-log-file"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("detached args = %v, want %v", got, want)
	}
}
