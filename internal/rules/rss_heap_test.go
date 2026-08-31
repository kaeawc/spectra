package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
)

func TestJVMRSSExceedsHeapFiresOnNativeExcess(t *testing.T) {
	s := baseSnap()
	// Committed ~195 MiB, RSS ~1269 MiB -> ~1074 MiB off-heap, 6.5x committed.
	s.JVMs = []jvm.Info{{PID: 10, MainClass: "svc.App", GC: &jvm.GCStats{OC: 200000}}}
	s.Processes = []process.Info{{PID: 10, RSSKiB: 1300000}}
	findings := ruleJVMRSSExceedsHeap().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "jvm-rss-exceeds-heap" || findings[0].Severity != SeverityLow {
		t.Fatalf("finding = %+v", findings[0])
	}
	if !strings.Contains(findings[0].Message, "off-heap/native") {
		t.Fatalf("message = %q", findings[0].Message)
	}
}

func TestJVMRSSExceedsHeapNoFireWhenRSSNearHeap(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 10, GC: &jvm.GCStats{OC: 200000}}}
	s.Processes = []process.Info{{PID: 10, RSSKiB: 250000}} // excess ~49 MiB, below floor
	if f := ruleJVMRSSExceedsHeap().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding when RSS ~ committed, got %v", f)
	}
}

func TestJVMRSSExceedsHeapNoFireForLargeHeapUnderRatio(t *testing.T) {
	s := baseSnap()
	// Committed ~3.8 GiB, RSS ~5.0 GiB: excess > 1 GiB but only 1.3x -> ratio guard.
	s.JVMs = []jvm.Info{{PID: 10, GC: &jvm.GCStats{OC: 4000000}}}
	s.Processes = []process.Info{{PID: 10, RSSKiB: 5200000}}
	if f := ruleJVMRSSExceedsHeap().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding below the ratio, got %v", f)
	}
}

func TestJVMRSSExceedsHeapNoFireWhenNoProcessMatch(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 10, GC: &jvm.GCStats{OC: 200000}}}
	s.Processes = []process.Info{{PID: 99, RSSKiB: 1300000}} // different PID
	if f := ruleJVMRSSExceedsHeap().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding without a PID match, got %v", f)
	}
}

func TestJVMRSSExceedsHeapNoFireWithoutGC(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{{PID: 10}} // no GC stats
	s.Processes = []process.Info{{PID: 10, RSSKiB: 1300000}}
	if f := ruleJVMRSSExceedsHeap().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no finding without GC stats, got %v", f)
	}
}
