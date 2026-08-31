package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestJVMGCAlgorithmEpsilonFires(t *testing.T) {
	s := baseSnap()
	s.Host.CPUCores = 8
	s.JVMs = []jvm.Info{{PID: 10, MainClass: "bench.App", VMArgs: "-XX:+UseEpsilonGC -Xmx512m"}}
	findings := ruleJVMGCAlgorithm().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 Epsilon finding, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].Message, "Epsilon") {
		t.Fatalf("message = %q", findings[0].Message)
	}
}

func TestJVMGCAlgorithmSerialLargeHeapFires(t *testing.T) {
	s := baseSnap()
	s.Host.CPUCores = 8
	s.JVMs = []jvm.Info{{PID: 10, MainClass: "svc.App", VMArgs: "-XX:+UseSerialGC -Xmx4g"}}
	findings := ruleJVMGCAlgorithm().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 SerialGC finding, got %d: %v", len(findings), findings)
	}
	if findings[0].Severity != SeverityMedium || !strings.Contains(findings[0].Message, "SerialGC") {
		t.Fatalf("finding = %+v", findings[0])
	}
}

func TestJVMGCAlgorithmSerialSmallHeapNoFire(t *testing.T) {
	s := baseSnap()
	s.Host.CPUCores = 8
	// Small desktop helper: SerialGC with a modest heap is legitimate.
	s.JVMs = []jvm.Info{{PID: 10, VMArgs: "-XX:+UseSerialGC -Xmx256m"}}
	if f := ruleJVMGCAlgorithm().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding for small-heap SerialGC, got %v", f)
	}
}

func TestJVMGCAlgorithmSerialLargeHeapSingleCoreNoFire(t *testing.T) {
	s := baseSnap()
	s.Host.CPUCores = 1 // a parallel collector can't help on a single core
	s.JVMs = []jvm.Info{{PID: 10, VMArgs: "-XX:+UseSerialGC -Xmx4g"}}
	if f := ruleJVMGCAlgorithm().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding on a single-core host, got %v", f)
	}
}

func TestJVMGCAlgorithmG1NoFire(t *testing.T) {
	s := baseSnap()
	s.Host.CPUCores = 8
	s.JVMs = []jvm.Info{{PID: 10, VMArgs: "-XX:+UseG1GC -Xmx8g"}}
	if f := ruleJVMGCAlgorithm().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding for G1, got %v", f)
	}
}
