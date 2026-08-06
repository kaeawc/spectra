package corefile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAbsolutePathTokens(t *testing.T) {
	probe := []byte("junk\x00/Library/Java/temurin.jdk/Contents/Home/bin/java\x00 more /usr/lib/system/libx.dylib\n/tmp/app")
	got := absolutePathTokens(probe)
	want := map[string]bool{
		"/Library/Java/temurin.jdk/Contents/Home/bin/java": true,
		"/usr/lib/system/libx.dylib":                       true,
		"/tmp/app":                                         true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Fatalf("missing tokens %v in %v", want, got)
	}
}

func TestResolveExecutableSkipsSharedLibsAndMissing(t *testing.T) {
	// Only /usr/lib and .dylib paths, plus a non-existent binary: nothing resolves.
	probe := []byte("/usr/lib/dyld\x00/opt/foo/libbar.dylib\x00/does/not/exist/java")
	if got := resolveExecutableFromProbe(probe, statExecutable); got != "" {
		t.Fatalf("resolved %q, want empty", got)
	}
}

func TestResolveExecutablePicksOnDiskExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "java")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A dylib appears first but must be skipped in favour of the executable.
	probe := []byte("/some/lib.dylib\x00" + exe + "\x00")
	if got := resolveExecutableFromProbe(probe, statExecutable); got != exe {
		t.Fatalf("resolved %q, want %q", got, exe)
	}
}

func TestInspectorAutoResolvesExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "java")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	corePath := filepath.Join(dir, "java.core")
	// HotSpot marker makes JVMAnalyzer support it; embedded java path is resolvable.
	content := "HotSpot java.lang.Thread\x00" + exe + "\x00"
	if err := os.WriteFile(corePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := (Inspector{Analyzers: []Analyzer{JVMAnalyzer{}}}).Inspect(context.Background(), corePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Artifact.ExecutablePath != exe {
		t.Fatalf("executable = %q, want %q (auto-resolved)", report.Artifact.ExecutablePath, exe)
	}
	if !report.Artifact.ExecutableResolved {
		t.Fatal("ExecutableResolved = false, want true")
	}
	// With the executable resolved, the JVM analyzer should now emit jhsdb commands.
	if len(report.Commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(report.Commands))
	}
}

func TestInspectorKeepsExplicitExecutable(t *testing.T) {
	dir := t.TempDir()
	corePath := filepath.Join(dir, "core")
	if err := os.WriteFile(corePath, []byte("HotSpot\x00/embedded/java\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := (Inspector{Analyzers: []Analyzer{JVMAnalyzer{}}}).Inspect(context.Background(), corePath, "/explicit/bin/java")
	if err != nil {
		t.Fatal(err)
	}
	if report.Artifact.ExecutablePath != "/explicit/bin/java" {
		t.Fatalf("executable = %q, want the explicit path", report.Artifact.ExecutablePath)
	}
	if report.Artifact.ExecutableResolved {
		t.Fatal("ExecutableResolved = true, want false for an explicit --exe")
	}
}
