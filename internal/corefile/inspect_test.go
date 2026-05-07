package corefile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectorRunsSupportingAnalyzers(t *testing.T) {
	dir := t.TempDir()
	corePath := filepath.Join(dir, "java.core")
	if err := os.WriteFile(corePath, []byte("prefix HotSpot java.lang.Thread suffix"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := (Inspector{Analyzers: []Analyzer{JVMAnalyzer{}}}).Inspect(context.Background(), corePath, "/Library/Java/JavaVirtualMachines/temurin.jdk/Contents/Home/bin/java")
	if err != nil {
		t.Fatal(err)
	}
	if report.Artifact.Path != corePath {
		t.Fatalf("path = %q, want %q", report.Artifact.Path, corePath)
	}
	if report.Runtime != "jvm" {
		t.Fatalf("runtime = %q, want jvm", report.Runtime)
	}
	if len(report.Commands) != 3 {
		t.Fatalf("commands len = %d, want 3", len(report.Commands))
	}
	if report.Commands[0].Tool != "jhsdb" {
		t.Fatalf("first tool = %q, want jhsdb", report.Commands[0].Tool)
	}
}

func TestJVMAnalyzerNeedsExecutableForCommands(t *testing.T) {
	report, err := (JVMAnalyzer{}).Analyze(context.Background(), Artifact{Path: "/tmp/core"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Commands) != 0 {
		t.Fatalf("commands len = %d, want 0", len(report.Commands))
	}
	if len(report.Observations) == 0 {
		t.Fatal("expected executable-path observation")
	}
}

func TestJVMAnalyzerDoesNotOwnUnknownCore(t *testing.T) {
	if (JVMAnalyzer{}).Supports(context.Background(), Artifact{Path: "/tmp/core"}, []byte("plain native crash")) {
		t.Fatal("JVM analyzer claimed an unknown core")
	}
}
