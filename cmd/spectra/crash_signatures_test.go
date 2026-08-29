package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashreport"
)

func rep(proc, exc, when, frame, incident string) sweptReport {
	r := &crashreport.Report{
		Process: proc, Exception: exc, Time: when, IncidentID: incident,
		FaultingThread: 0,
		Threads:        []crashreport.Thread{{Triggered: true, Frames: []string{frame}}},
	}
	return sweptReport{File: "/r/" + incident + ".ips", Report: r}
}

func TestAggregateSignaturesCollapsesRepeats(t *testing.T) {
	swept := []sweptReport{
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-01 09:00:00.00 -0500", "MyApp`flush + 1", "a"),
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-05 15:00:00.00 -0500", "MyApp`flush + 1", "b"),
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-03 12:00:00.00 -0500", "MyApp`flush + 1", "c"),
		rep("Other", "EXC_CRASH", "2026-08-02 10:00:00.00 -0500", "Other`boom + 9", "d"),
	}
	sigs := aggregateSignatures(swept)
	if len(sigs) != 2 {
		t.Fatalf("signatures = %d, want 2: %+v", len(sigs), sigs)
	}
	top := sigs[0]
	if top.Count != 3 || top.Process != "MyApp" {
		t.Errorf("top signature = %+v, want MyApp x3", top)
	}
	if !strings.HasPrefix(top.First, "2026-08-01") || !strings.HasPrefix(top.Last, "2026-08-05") {
		t.Errorf("first/last = %q/%q, want 08-01/08-05", top.First, top.Last)
	}
	// last-seen sample should be the most recent (incident b)
	if top.Incident != "b" {
		t.Errorf("sample incident = %q, want b (most recent)", top.Incident)
	}
}

func TestAggregateSignaturesNoFrameBucket(t *testing.T) {
	r := &crashreport.Report{Process: "P", Exception: "EXC_CRASH", Time: "2026-08-01 09:00:00.00 -0500", Threads: nil}
	sigs := aggregateSignatures([]sweptReport{{File: "/r/x.ips", Report: r}})
	if len(sigs) != 1 || sigs[0].TopFrame != "(no frame)" {
		t.Errorf("expected a (no frame) bucket, got %+v", sigs)
	}
}

func TestRunCrashSignaturesRenders(t *testing.T) {
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"a.ips", "b.ips"}, nil },
		read: func(f string) ([]byte, error) {
			when := "2026-08-05 10:00:00.00 -0500"
			if f == "a.ips" {
				when = "2026-08-01 10:00:00.00 -0500"
			}
			return []byte(ipsReport("MyApp", when, "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashSignaturesWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "[2×] MyApp") {
		t.Errorf("expected a 2x MyApp signature; got:\n%s", s)
	}
}

func TestRunCrashSignaturesEmpty(t *testing.T) {
	deps := crashListDeps{list: func() ([]string, error) { return nil, nil }, read: func(string) ([]byte, error) { return nil, nil }}
	var out, errBuf bytes.Buffer
	runCrashSignaturesWithDeps(false, 25, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "No crash reports found") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunCrashSignaturesRejectsPositional(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashSignatures([]string{"extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
