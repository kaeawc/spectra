package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/oom"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestJVMOOMDetectedFiresPerVariant(t *testing.T) {
	s := baseSnap()
	s.OOMReports = []snapshot.OOMReport{{
		PID:       10,
		MainClass: "svc.App",
		LogPath:   "/tmp/app.log",
		Events: []oom.Event{
			{Variant: oom.VariantHeapSpace, LineNo: 2},
			{Variant: oom.VariantHeapSpace, LineNo: 40}, // duplicate variant -> deduped
			{Variant: oom.VariantMetaspace, LineNo: 88},
		},
	}}
	findings := ruleJVMOOMDetected().MatchFn(s)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (heap + metaspace, heap deduped), got %d: %v", len(findings), findings)
	}
	for _, f := range findings {
		if f.RuleID != "jvm-oom-detected" || f.Severity != SeverityHigh {
			t.Fatalf("finding = %+v", f)
		}
		if !strings.Contains(f.Message, "/tmp/app.log") {
			t.Errorf("message should name the log path: %q", f.Message)
		}
	}
	// Distinct subjects so the issue catalog tracks the two variants separately.
	if findings[0].Subject == findings[1].Subject {
		t.Fatalf("findings share a subject: %q", findings[0].Subject)
	}
}

func TestJVMOOMDetectedNoFireWhenEmpty(t *testing.T) {
	if f := ruleJVMOOMDetected().MatchFn(baseSnap()); len(f) != 0 {
		t.Fatalf("expected no findings with no OOM reports, got %v", f)
	}
}
