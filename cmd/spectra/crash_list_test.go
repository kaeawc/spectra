package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashquery"
)

func ipsReport(app, when, exc string) string {
	return `{"app_name":"` + app + `","timestamp":"` + when + `","bug_type":"309","incident_id":"INC-` + app + `","name":"` + app + `"}
{"procName":"` + app + `","pid":1,"exception":{"type":"` + exc + `","signal":"SIGSEGV"},"termination":{"indicator":"x"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestRunCrashListNewestFirst(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"old.ips", "new.ips"}, nil },
		Read: func(f string) ([]byte, error) {
			if f == "new.ips" {
				return []byte(ipsReport("NewApp", "2026-08-05 10:00:00.00 -0500", "EXC_BREAKPOINT")), nil
			}
			return []byte(ipsReport("OldApp", "2026-01-09 21:20:32.00 -0600", "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashListWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	ni, oi := strings.Index(s, "NewApp"), strings.Index(s, "OldApp")
	if ni < 0 || oi < 0 || ni > oi {
		t.Errorf("expected NewApp before OldApp; got:\n%s", s)
	}
}

func TestRunCrashListShowsFileAndIncident(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"/reports/MyApp-2026.ips"}, nil },
		Read: func(string) ([]byte, error) {
			return []byte(ipsReport("MyApp", "2026-08-05 10:00:00.00 -0500", "EXC_CRASH")), nil
		},
	}
	var out, errBuf bytes.Buffer
	runCrashListWithDeps(false, 25, &out, &errBuf, deps)
	s := out.String()
	if !strings.Contains(s, "/reports/MyApp-2026.ips") {
		t.Errorf("text output should show the source file for follow-up; got:\n%s", s)
	}
	if !strings.Contains(s, "incident INC-MyApp") {
		t.Errorf("text output should show the incident id; got:\n%s", s)
	}
}

func TestRunCrashListRejectsPositional(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashList([]string{"extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2 for a stray positional arg", code)
	}
}

func TestRunCrashListJSON(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"a.ips"}, nil },
		Read: func(string) ([]byte, error) {
			return []byte(ipsReport("Solo", "2026-08-05 10:00:00.00 -0500", "EXC_CRASH")), nil
		},
	}
	var out, errBuf bytes.Buffer
	runCrashListWithDeps(true, 25, &out, &errBuf, deps)
	if !strings.Contains(out.String(), `"process": "Solo"`) {
		t.Errorf("json = %q", out.String())
	}
}

func TestRunCrashListEmpty(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return nil, nil },
		Read: func(string) ([]byte, error) { return nil, nil },
	}
	var out, errBuf bytes.Buffer
	if code := runCrashListWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "No crash reports found") {
		t.Errorf("out = %q", out.String())
	}
}
