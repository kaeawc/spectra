package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashready"
	"github.com/kaeawc/spectra/internal/detect"
)

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
