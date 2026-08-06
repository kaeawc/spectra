package rules

import (
	"testing"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

// fdSnap builds a snapshot with the given soft fd limit, kern.maxfiles sysctl
// (omitted when empty), and processes.
func fdSnap(soft int, maxfiles string, procs ...process.Info) snapshot.Snapshot {
	s := baseSnap()
	s.FDLimit = sysinfo.FDLimit{Soft: soft}
	s.Processes = procs
	if maxfiles != "" {
		s.Sysctls = map[string]string{"kern.maxfiles": maxfiles}
	}
	return s
}

func fdProc(pid int, name string, openFDs int) process.Info {
	return process.Info{PID: pid, Command: name, OpenFDs: openFDs}
}

func findingsFor(s snapshot.Snapshot) []Finding {
	return ruleFDPressure().MatchFn(s)
}

func TestFDPressurePerProcess(t *testing.T) {
	cases := []struct {
		name         string
		soft         int
		openFDs      int
		wantFinding  bool
		wantSeverity Severity
	}{
		{"below warn threshold", 1000, 799, false, ""},
		{"exactly at warn threshold", 1000, 800, true, SeverityMedium},
		{"mid warn band", 256, 240, true, SeverityMedium}, // 93%
		{"just below critical", 1000, 949, true, SeverityMedium},
		{"exactly at critical", 1000, 950, true, SeverityHigh},
		{"above critical", 256, 256, true, SeverityHigh}, // 100%
		{"soft limit zero", 0, 100000, false, ""},
		{"open fds zero", 1000, 0, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fdSnap(tc.soft, "", fdProc(42, "leaky", tc.openFDs))
			findings := findingsFor(s)
			if !tc.wantFinding {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %+v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
			}
			if findings[0].Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", findings[0].Severity, tc.wantSeverity)
			}
			if findings[0].Subject != "PID 42 (leaky)" {
				t.Errorf("subject = %q, want %q", findings[0].Subject, "PID 42 (leaky)")
			}
			if findings[0].RuleID != "fd-pressure" {
				t.Errorf("rule id = %q, want fd-pressure", findings[0].RuleID)
			}
		})
	}
}

func TestFDPressureSystemWide(t *testing.T) {
	cases := []struct {
		name         string
		maxfiles     string
		openFDs      []int
		wantSystem   bool
		wantSeverity Severity
	}{
		{"sum at 80% of maxfiles", "1000", []int{500, 300}, true, SeverityHigh},
		{"sum above maxfiles", "1000", []int{700, 500}, true, SeverityHigh},
		{"sum below 80%", "1000", []int{400, 300}, false, ""},
		{"maxfiles missing", "", []int{5000, 5000}, false, ""},
		{"maxfiles unparseable", "unlimited", []int{5000, 5000}, false, ""},
		{"maxfiles zero", "0", []int{5000, 5000}, false, ""},
		{"no open fds", "1000", []int{0, 0}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Soft = 0 so per-process findings never fire; isolate the system rule.
			procs := make([]process.Info, len(tc.openFDs))
			for i, fd := range tc.openFDs {
				procs[i] = fdProc(100+i, "proc", fd)
			}
			s := fdSnap(0, tc.maxfiles, procs...)
			findings := findingsFor(s)
			if !tc.wantSystem {
				if len(findings) != 0 {
					t.Fatalf("expected no findings, got %+v", findings)
				}
				return
			}
			if len(findings) != 1 {
				t.Fatalf("expected 1 system finding, got %d: %+v", len(findings), findings)
			}
			if findings[0].Subject != "system file table" {
				t.Errorf("subject = %q, want %q", findings[0].Subject, "system file table")
			}
			if findings[0].Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", findings[0].Severity, tc.wantSeverity)
			}
		})
	}
}

// TestFDPressureBothFindings verifies per-process and system findings coexist.
func TestFDPressureBothFindings(t *testing.T) {
	s := fdSnap(1000, "2000", fdProc(1, "a", 990), fdProc(2, "b", 900))
	findings := findingsFor(s)
	// PID 1 at 99% (high), PID 2 at 90% (medium), plus system 1890/2000=94% (high).
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings, got %d: %+v", len(findings), findings)
	}
	var sawSystem bool
	for _, f := range findings {
		if f.Subject == "system file table" {
			sawSystem = true
		}
	}
	if !sawSystem {
		t.Errorf("expected a system-wide finding among %+v", findings)
	}
}
