package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestJVMOOMDumpDisabledFiresForTunedService(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{
		PID:       10,
		MainClass: "com.acme.Server",
		VMArgs:    "-Xmx512m",
	}}
	findings := ruleJVMOOMDumpDisabled().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "jvm-oom-dump-disabled" || findings[0].Severity != SeverityLow {
		t.Fatalf("finding = %+v", findings[0])
	}
}

func TestJVMOOMDumpDisabledNoFireWhenEnabled(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{
		PID:       10,
		MainClass: "com.acme.Server",
		VMArgs:    "-Xmx512m -XX:+HeapDumpOnOutOfMemoryError",
	}}
	if f := ruleJVMOOMDumpDisabled().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding when the flag is set, got %v", f)
	}
}

func TestJVMOOMDumpDisabledNoFireWithoutXmx(t *testing.T) {
	s := baseSnap()
	// Untuned / likely transient CLI JVM: no -Xmx -> don't nag.
	s.JVMs = []jvm.Info{{PID: 10, MainClass: "com.acme.OneShot", VMArgs: "-Dfoo=bar"}}
	if f := ruleJVMOOMDumpDisabled().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding without -Xmx, got %v", f)
	}
}

func TestJVMOOMDumpDisabledNoFireForDevTooling(t *testing.T) {
	s := baseSnap()
	// Gradle daemon (build-tool-daemon profile) with a tuned heap and no flag
	// must be excluded as dev tooling.
	s.JVMs = []jvm.Info{{
		PID:       10,
		MainClass: "org.gradle.launcher.daemon.bootstrap.GradleDaemon",
		VMArgs:    "-Xmx2g",
	}}
	if f := ruleJVMOOMDumpDisabled().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding for a build-tool daemon, got %v", f)
	}
}
