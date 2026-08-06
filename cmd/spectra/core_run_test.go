package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// jvmCoreFixture writes an on-disk java executable and a synthetic core that
// embeds its path plus a HotSpot marker, so the inspector auto-resolves the exe.
func jvmCoreFixture(t *testing.T) (corePath, exe string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "java")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	corePath = filepath.Join(dir, "java.core")
	if err := os.WriteFile(corePath, []byte("HotSpot java.lang.Thread\x00"+exe+"\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return corePath, exe
}

func TestCoreRunExecutesJstack(t *testing.T) {
	corePath, exe := jvmCoreFixture(t)

	var gotName string
	var gotArgs []string
	fake := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("thread state output\n"), nil
	}

	var stdout, stderr bytes.Buffer
	code := coreRun(context.Background(), &stdout, &stderr, "jstack", corePath, "", fake)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	if gotName != "jhsdb" {
		t.Fatalf("tool = %q, want jhsdb", gotName)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.HasPrefix(joined, "jstack ") || !strings.Contains(joined, "--exe "+exe) || !strings.Contains(joined, "--core "+corePath) {
		t.Fatalf("args = %q", joined)
	}
	if !strings.Contains(stdout.String(), "thread state output") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestCoreRunUnsupportedAction(t *testing.T) {
	corePath, _ := jvmCoreFixture(t)
	var stdout, stderr bytes.Buffer
	called := false
	fake := func(_ context.Context, _ string, _ ...string) ([]byte, error) { called = true; return nil, nil }
	code := coreRun(context.Background(), &stdout, &stderr, "jstat", corePath, "", fake)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if called {
		t.Fatal("runner should not be called for an unsupported action")
	}
}

func TestCoreRunNoExecutable(t *testing.T) {
	// Core with no embedded on-disk executable and no --exe: refuse to run.
	dir := t.TempDir()
	corePath := filepath.Join(dir, "core")
	if err := os.WriteFile(corePath, []byte("HotSpot java.lang.Thread\x00/nope/java\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := coreRun(context.Background(), &stdout, &stderr, "jstack", corePath, "", func(context.Context, string, ...string) ([]byte, error) { return nil, nil })
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no executable resolved") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
