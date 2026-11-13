package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashready"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
)

func TestRunCrashMacOnlyPartsUnsupportedOnLinux(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	for _, args := range [][]string{{}, {"readiness"}, {"list"}, {"signatures"}, {"oom"}} {
		var out, errBuf bytes.Buffer
		if code := runCrashWithIO(args, &out, &errBuf, nil); code != 1 {
			t.Errorf("%v: exit = %d, want 1", args, code)
		}
		if !strings.Contains(errBuf.String(), "unsupported on host OS (linux)") {
			t.Errorf("%v: stderr = %q", args, errBuf.String())
		}
	}
	// An explicit --dir of copied reports is plain file parsing and still works.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ips"), []byte(ipsReport("App", "2026-08-05 10:00:00.00 -0500", "EXC_BAD_ACCESS")), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	if code := runCrashWithIO([]string{"list", "--dir", dir}, &out, &errBuf, nil); code != 0 || !strings.Contains(out.String(), "App") {
		t.Fatalf("list --dir: exit=%d out=%q stderr=%q", code, out.String(), errBuf.String())
	}
}

func TestRunCrashUnknownSubcommand(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runCrashWithIO([]string{"bogus"}, &out, &errBuf, detect.Detect)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errBuf.String(), "unknown crash subcommand") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestHasEntitlement(t *testing.T) {
	ents := []string{"app-sandbox", "get-task-allow", "network.client"}
	if !hasEntitlement(ents, "get-task-allow") {
		t.Error("expected get-task-allow present")
	}
	if hasEntitlement(ents, "virtualization") {
		t.Error("did not expect virtualization")
	}
}

func TestCrashStatusLabel(t *testing.T) {
	cases := map[crashready.Status]string{
		crashready.StatusOK:       "ok",
		crashready.StatusWarn:     "warn",
		crashready.StatusCritical: "CRIT",
	}
	for s, want := range cases {
		if got := crashStatusLabel(s); got != want {
			t.Errorf("crashStatusLabel(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestRenderCrashReadiness(t *testing.T) {
	r := crashready.Report{
		App: "MyApp",
		Checks: []crashready.Check{
			{Name: "kern.coredump", Status: crashready.StatusWarn, Detail: "disabled", Fix: "sudo sysctl kern.coredump=1"},
			{Name: "app: MyApp", Status: crashready.StatusOK, Detail: "debuggable"},
		},
	}
	var out bytes.Buffer
	renderCrashReadiness(&out, r)
	s := out.String()
	for _, want := range []string{"MyApp", "warn", "kern.coredump", "fix: sudo sysctl", "ready:"} {
		if !strings.Contains(s, want) {
			t.Errorf("render output missing %q; got:\n%s", want, s)
		}
	}
}

func TestRenderCrashReadinessCritical(t *testing.T) {
	r := crashready.Report{Checks: []crashready.Check{
		{Name: "/cores", Status: crashready.StatusCritical, Detail: "unwritable"},
	}}
	var out bytes.Buffer
	renderCrashReadiness(&out, r)
	if !strings.Contains(out.String(), "NOT ready") {
		t.Errorf("critical report should say NOT ready; got:\n%s", out.String())
	}
}
