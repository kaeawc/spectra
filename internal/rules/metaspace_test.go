package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestJVMMetaspacePressureFiresNearMaxMetaspaceSize(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{
		PID:       10,
		MainClass: "leak.App",
		VMArgs:    "-XX:MaxMetaspaceSize=256m",
		// 240000 KiB used = ~234 MiB, ~91% of the 256 MiB ceiling.
		GC: &jvm.GCStats{MC: 245000, MU: 240000},
	}}
	findings := ruleJVMMetaspacePressure().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 metaspace finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "jvm-metaspace-pressure" || findings[0].Severity != SeverityMedium {
		t.Fatalf("finding = %+v", findings[0])
	}
	if !strings.Contains(findings[0].Message, "MaxMetaspaceSize") {
		t.Fatalf("message should name the ceiling: %q", findings[0].Message)
	}
}

func TestJVMMetaspacePressureNoFireWhenCeilingUnset(t *testing.T) {
	s := baseSnap()
	// High MU/MC ratio but no configured ceiling -> committed just tracks used;
	// this must NOT fire (it would be pure noise).
	s.JVMs = []jvm.Info{{PID: 10, VMArgs: "-Xmx1g", GC: &jvm.GCStats{MC: 250000, MU: 249000}}}
	if f := ruleJVMMetaspacePressure().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no findings without a ceiling, got %v", f)
	}
}

func TestJVMMetaspacePressureNoFireBelowThreshold(t *testing.T) {
	s := baseSnap()
	// 200000 KiB used = ~195 MiB, ~76% of the 256 MiB ceiling -> below 90%.
	s.JVMs = []jvm.Info{{PID: 10, VMArgs: "-XX:MaxMetaspaceSize=256m", GC: &jvm.GCStats{MC: 210000, MU: 200000}}}
	if f := ruleJVMMetaspacePressure().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no findings below threshold, got %v", f)
	}
}

func TestJVMMetaspacePressureFiresForCompressedClassSpace(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{
		PID:       10,
		MainClass: "ccs.App",
		VMArgs:    "-XX:CompressedClassSpaceSize=64m",
		// 61000 KiB used = ~59.6 MiB, ~93% of the 64 MiB ceiling.
		GC: &jvm.GCStats{CCSC: 62000, CCSU: 61000},
	}}
	findings := ruleJVMMetaspacePressure().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 compressed-class-space finding, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0].Message, "Compressed class space") {
		t.Fatalf("message should name compressed class space: %q", findings[0].Message)
	}
}

func TestParseVMArgsMetaspaceCeilings(t *testing.T) {
	f := ParseVMArgs("-XX:MaxMetaspaceSize=512m -XX:CompressedClassSpaceSize=128m")
	if f.MaxMetaspaceSizeBytes != 512*1024*1024 {
		t.Errorf("MaxMetaspaceSizeBytes = %d, want %d", f.MaxMetaspaceSizeBytes, 512*1024*1024)
	}
	if f.CompressedClassSpaceSizeBytes != 128*1024*1024 {
		t.Errorf("CompressedClassSpaceSizeBytes = %d, want %d", f.CompressedClassSpaceSizeBytes, 128*1024*1024)
	}
	// Unset -> 0 sentinel.
	if empty := ParseVMArgs("-Xmx1g"); empty.MaxMetaspaceSizeBytes != 0 || empty.CompressedClassSpaceSizeBytes != 0 {
		t.Errorf("unset ceilings should be 0, got %+v", empty)
	}
}
