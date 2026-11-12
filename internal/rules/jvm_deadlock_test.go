package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/threadinspect"
)

func deadlockCycle() threadinspect.DeadlockCycle {
	return threadinspect.DeadlockCycle{
		Threads: []string{"worker-2", "worker-1"},
		Locks:   []string{"<0x2> (a java.lang.Object)", "<0x1> (a java.lang.Object)"},
		Waits: []threadinspect.DeadlockWait{
			{Thread: "worker-2", Lock: "<0x2> (a java.lang.Object)", HeldBy: "worker-1"},
			{Thread: "worker-1", Lock: "<0x1> (a java.lang.Object)", HeldBy: "worker-2"},
		},
	}
}

func TestJVMDeadlockFiresPerCycle(t *testing.T) {
	s := baseSnap()
	s.JVMDeadlocks = []snapshot.JVMDeadlockReport{
		{PID: 10, MainClass: "svc.App", Cycles: []threadinspect.DeadlockCycle{
			deadlockCycle(),
			{Threads: []string{"pool-1", "pool-2"}},
		}},
	}
	findings := ruleJVMDeadlock().MatchFn(s)
	if len(findings) != 2 {
		t.Fatalf("expected one finding per cycle, got %d: %v", len(findings), findings)
	}
	f := findings[0]
	if f.RuleID != "jvm-deadlock" || f.Severity != SeverityHigh {
		t.Fatalf("finding = %+v", f)
	}
	if f.Subject != "PID 10 (svc.App) deadlock: worker-1, worker-2" {
		t.Errorf("subject = %q", f.Subject)
	}
	want := `"worker-2" waits for <0x2> (a java.lang.Object) held by "worker-1"; "worker-1" waits for <0x1> (a java.lang.Object) held by "worker-2"`
	if !strings.Contains(f.Message, want) {
		t.Errorf("message should name threads and lock cycle:\n got %q\nwant substring %q", f.Message, want)
	}
	if !strings.Contains(f.Fix, "spectra jvm thread-dump 10") {
		t.Errorf("fix should point at the thread dump: %q", f.Fix)
	}
	if !strings.Contains(findings[1].Message, `"pool-1" -> "pool-2"`) {
		t.Errorf("fallback message should name threads: %q", findings[1].Message)
	}
	if findings[0].Subject == findings[1].Subject {
		t.Fatalf("cycles share a subject: %q", findings[0].Subject)
	}
}

func TestJVMDeadlockNoFireWithoutCycles(t *testing.T) {
	s := baseSnap()
	if f := ruleJVMDeadlock().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no findings without deadlock checks, got %v", f)
	}
	s.JVMDeadlocks = []snapshot.JVMDeadlockReport{{PID: 10, MainClass: "svc.App"}}
	if f := ruleJVMDeadlock().MatchFn(s); len(f) != 0 {
		t.Fatalf("expected no findings for a clean thread dump, got %v", f)
	}
}

func TestJVMDeadlockInV1Catalog(t *testing.T) {
	for _, r := range V1Catalog() {
		if r.ID == "jvm-deadlock" {
			if r.Severity != SeverityHigh {
				t.Fatalf("jvm-deadlock severity = %s, want high", r.Severity)
			}
			return
		}
	}
	t.Fatal("jvm-deadlock missing from V1Catalog")
}
