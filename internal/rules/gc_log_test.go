package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/gclog"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func gcLogSnap(sum gclog.Summary) snapshot.Snapshot {
	s := baseSnap()
	s.GCLogReports = []snapshot.GCLogReport{{
		PID:       10,
		MainClass: "svc.App",
		LogPaths:  []string{"/var/log/svc/gc.log"},
		Summary:   sum,
	}}
	return s
}

func TestJVMGCLogPressureFires(t *testing.T) {
	cases := map[string]struct {
		sum  gclog.Summary
		want string
	}{
		"full gc":        {gclog.Summary{Pauses: 3, FullGCCount: 1, MaxPauseMs: 50}, "Full GC"},
		"evac failure":   {gclog.Summary{Pauses: 3, EvacuationFailures: 2, MaxPauseMs: 50}, "evacuation failure"},
		"max pause":      {gclog.Summary{Pauses: 3, MaxPauseMs: 750}, "max pause"},
		"total pause":    {gclog.Summary{Pauses: 400, MaxPauseMs: 40, TotalPauseMs: 12_000}, "total pause"},
		"system.gc full": {gclog.Summary{Pauses: 1, FullGCCount: 1, SystemGCCount: 1}, "Full GC"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			findings := ruleJVMGCLogPressure().MatchFn(gcLogSnap(tc.sum))
			if len(findings) != 1 {
				t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
			}
			f := findings[0]
			if f.RuleID != "jvm-gc-log-pressure" || f.Severity != SeverityMedium || f.Subject != "PID 10 (svc.App)" {
				t.Fatalf("finding = %+v", f)
			}
			if !strings.Contains(f.Message, tc.want) || !strings.Contains(f.Message, "/var/log/svc/gc.log") {
				t.Fatalf("message %q missing %q or log path", f.Message, tc.want)
			}
		})
	}
}

func TestJVMGCLogPressureSystemGCFix(t *testing.T) {
	f := ruleJVMGCLogPressure().MatchFn(gcLogSnap(gclog.Summary{Pauses: 1, FullGCCount: 1, SystemGCCount: 1}))
	if len(f) != 1 || !strings.Contains(f[0].Fix, "DisableExplicitGC") {
		t.Fatalf("expected System.gc() remediation, got %+v", f)
	}
}

func TestJVMGCLogPressureNoFireOnHealthyLog(t *testing.T) {
	healthy := gclog.Summary{Pauses: 200, YoungGCCount: 200, TotalPauseMs: 900, MaxPauseMs: 12}
	if f := ruleJVMGCLogPressure().MatchFn(gcLogSnap(healthy)); len(f) != 0 {
		t.Fatalf("expected no findings for a healthy log, got %v", f)
	}
	if f := ruleJVMGCLogPressure().MatchFn(baseSnap()); len(f) != 0 {
		t.Fatalf("expected no findings without GC logs, got %v", f)
	}
}

func pressuredJVM(vmArgs string) jvm.Info {
	return jvm.Info{
		PID:       10,
		MainClass: "com.acme.Server",
		VMArgs:    vmArgs,
		GC:        &jvm.GCStats{OC: 1000, OU: 960, FGC: 2},
	}
}

func TestJVMGCLoggingDisabledFires(t *testing.T) {
	s := baseSnap()
	s.JVMs = []jvm.Info{pressuredJVM("-Xmx2g -XX:+UseG1GC")}
	findings := ruleJVMGCLoggingDisabled().MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	if findings[0].RuleID != "jvm-gc-logging-disabled" || findings[0].Severity != SeverityLow {
		t.Fatalf("finding = %+v", findings[0])
	}
	if !strings.Contains(findings[0].Fix, "-Xlog:gc") {
		t.Fatalf("fix should recommend -Xlog:gc: %q", findings[0].Fix)
	}
}

func TestJVMGCLoggingDisabledFiresOnFullGCBurst(t *testing.T) {
	j := pressuredJVM("-Xmx2g")
	j.GC = &jvm.GCStats{OC: 1000, OU: 100, FGC: FullGCBurstCount, FGCT: FullGCBurstSeconds}
	s := baseSnap()
	s.JVMs = []jvm.Info{j}
	if f := ruleJVMGCLoggingDisabled().MatchFn(s); len(f) != 1 {
		t.Fatalf("expected 1 finding on a full-GC burst, got %v", f)
	}
}

func TestJVMGCLoggingDisabledNoFire(t *testing.T) {
	calm := pressuredJVM("-Xmx2g")
	calm.GC = &jvm.GCStats{OC: 1000, OU: 300}
	noGC := pressuredJVM("-Xmx2g")
	noGC.GC = nil
	cases := map[string]struct {
		j       jvm.Info
		reports []snapshot.GCLogReport
	}{
		"unified xlog":     {j: pressuredJVM("-Xmx2g -Xlog:gc*:file=/tmp/gc.log")},
		"verbose gc":       {j: pressuredJVM("-Xmx2g -verbose:gc")},
		"legacy loggc":     {j: pressuredJVM("-Xmx2g -Xloggc:/tmp/gc.log -XX:+PrintGCDetails")},
		"no symptoms":      {j: calm},
		"no jstat":         {j: noGC},
		"unknown args":     {j: pressuredJVM("")},
		"tight by design":  {j: pressuredJVM("-Xmx2g -XX:MaxHeapFreeRatio=10")},
		"gc log was found": {j: pressuredJVM("-Xmx2g"), reports: []snapshot.GCLogReport{{PID: 10}}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := baseSnap()
			s.JVMs = []jvm.Info{tc.j}
			s.GCLogReports = tc.reports
			if f := ruleJVMGCLoggingDisabled().MatchFn(s); len(f) != 0 {
				t.Fatalf("expected no findings, got %v", f)
			}
		})
	}
}

func TestEnablesGCLogging(t *testing.T) {
	cases := map[string]bool{
		"-Xlog":                               true,
		"-Xlog:gc":                            true,
		"-Xlog:gc*":                           true,
		"-Xlog:gc*=debug:file=gc.log":         true,
		"-Xlog:safepoint,gc+heap=info:stdout": true,
		"-Xlog:all=info":                      true,
		"-verbose:gc":                         true,
		"-XX:+PrintGC":                        true,
		"-XX:+PrintGCDetails":                 true,
		"-Xloggc:/tmp/gc.log":                 true,
		"-Xlog:safepoint":                     false,
		"-Xlog:disable":                       false,
		"-Xlog:gc=off":                        false,
		"-XX:-PrintGC":                        false,
		"-Xmx1g":                              false,
	}
	for tok, want := range cases {
		if got := enablesGCLogging(tok); got != want {
			t.Errorf("enablesGCLogging(%q) = %v, want %v", tok, got, want)
		}
	}
	if !ParseVMArgs("-Xmx1g -Xlog:gc:stdout").GCLogging {
		t.Error("ParseVMArgs should set GCLogging for -Xlog:gc")
	}
}
