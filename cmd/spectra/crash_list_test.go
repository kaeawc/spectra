package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ipsReport(app, when, exc string) string {
	return `{"app_name":"` + app + `","timestamp":"` + when + `","bug_type":"309","incident_id":"INC-` + app + `","name":"` + app + `"}
{"procName":"` + app + `","pid":1,"exception":{"type":"` + exc + `","signal":"SIGSEGV"},"termination":{"indicator":"x"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestSweepSkipsUnparseable(t *testing.T) {
	read := func(f string) ([]byte, error) {
		switch f {
		case "good.ips":
			return []byte(ipsReport("Good", "2026-08-05 10:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		case "legacy.crash":
			return []byte("Process: Old [1]\n"), nil // legacy plain-text
		default:
			return nil, os.ErrNotExist
		}
	}
	swept, skipped := sweepCrashReports([]string{"good.ips", "legacy.crash", "missing.ips"}, read)
	if len(swept) != 1 || swept[0].Report.Process != "Good" {
		t.Errorf("swept = %+v, want 1 (Good)", swept)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (legacy + missing)", skipped)
	}
}

func TestRunCrashListNewestFirst(t *testing.T) {
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"old.ips", "new.ips"}, nil },
		read: func(f string) ([]byte, error) {
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
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"/reports/MyApp-2026.ips"}, nil },
		read: func(string) ([]byte, error) {
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
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"a.ips"}, nil },
		read: func(string) ([]byte, error) {
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
	deps := crashListDeps{
		list: func() ([]string, error) { return nil, nil },
		read: func(string) ([]byte, error) { return nil, nil },
	}
	var out, errBuf bytes.Buffer
	if code := runCrashListWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "No crash reports found") {
		t.Errorf("out = %q", out.String())
	}
}

func TestFindIPSFilesWalksRetiredAndDotfiles(t *testing.T) {
	dir := t.TempDir()
	retired := filepath.Join(dir, "Retired")
	if err := os.Mkdir(retired, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "App-2026.ips"))
	write(filepath.Join(dir, ".hidden-2026.ips")) // dotfile report
	write(filepath.Join(retired, "Old-2026.ips"))
	write(filepath.Join(dir, "power.diag")) // must be ignored

	got, err := findIPSFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("found %d .ips, want 3: %v", len(got), got)
	}
	for _, g := range got {
		if filepath.Ext(g) != ".ips" {
			t.Errorf("non-.ips in results: %s", g)
		}
	}
}

func TestFindIPSFilesMissingDir(t *testing.T) {
	got, err := findIPSFiles(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil || got != nil {
		t.Errorf("missing dir = %v, %v; want nil, nil", got, err)
	}
}
