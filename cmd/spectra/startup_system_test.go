package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/helper"
	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/startup"
)

type fakeLaunchdClient struct {
	prep       helper.LaunchdPrepareResult
	prepErr    error
	change     helper.LaunchdChangeResult
	changeErr  error
	prepared   []string
	executed   []string
	gotActions []string
}

func (f *fakeLaunchdClient) LaunchdSystemPrepare(label, action string) (helper.LaunchdPrepareResult, error) {
	f.prepared = append(f.prepared, action+" "+label)
	return f.prep, f.prepErr
}

func (f *fakeLaunchdClient) LaunchdSystemChange(action, token string) (helper.LaunchdChangeResult, error) {
	f.executed = append(f.executed, token)
	f.gotActions = append(f.gotActions, action)
	return f.change, f.changeErr
}

func okPrepare() helper.LaunchdPrepareResult {
	return helper.LaunchdPrepareResult{
		Label: "com.example.agentd", Action: "disable", Token: "tok123",
		Target: &helper.LaunchdTarget{PlistPath: "/Library/LaunchDaemons/com.example.agentd.plist", Program: "/Applications/Example.app/Contents/MacOS/agentd",
			BundlePath: "/Applications/Example.app", BundleID: "com.example.app", TeamID: "ABCDE12345"},
		Before:  &helper.LaunchdState{Loaded: true, Running: true, PID: 412},
		Planned: [][]string{{"/bin/launchctl", "disable", "system/com.example.agentd"}, {"/bin/launchctl", "bootout", "system/com.example.agentd"}},
	}
}

func okChange() helper.LaunchdChangeResult {
	return helper.LaunchdChangeResult{
		Action: "disable", Label: "com.example.agentd", Domain: "system", Via: "spectra-helper", Outcome: helper.OutcomeOK,
		Commands: []startup.Command{{Argv: []string{"/bin/launchctl", "disable", "system/com.example.agentd"}}},
		After:    &helper.LaunchdState{Disabled: true},
	}
}

func runSystemStartup(t *testing.T, args []string, c *fakeLaunchdClient, tty bool, stdin string) (int, string, string) {
	t.Helper()
	opts, _ := startupTestOpts(t)
	var out, errOut bytes.Buffer
	sys := startupSystem{
		client: func() launchdHelperClient { return c },
		stdin:  strings.NewReader(stdin),
		isTTY:  func() bool { return tty },
	}
	code := runStartupWith(args, &out, &errOut, opts, sys)
	return code, out.String(), errOut.String()
}

func TestStartupSystemTTYYes(t *testing.T) {
	c := &fakeLaunchdClient{prep: okPrepare(), change: okChange()}
	code, out, errOut := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system"}, c, true, "y\n")
	if code != startupExitOK {
		t.Fatalf("exit %d: %s %s", code, out, errOut)
	}
	for _, want := range []string{"ABCDE12345", "/bin/launchctl bootout system/com.example.agentd", "Disable com.example.agentd for all users? [y/N]", "via spectra-helper", "loaded,running (pid 412)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if len(c.executed) != 1 || c.executed[0] != "tok123" || c.gotActions[0] != "disable" {
		t.Fatalf("executed = %v %v", c.executed, c.gotActions)
	}
}

func TestStartupSystemTTYNo(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", ""} {
		c := &fakeLaunchdClient{prep: okPrepare(), change: okChange()}
		code, out, _ := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system"}, c, true, answer)
		if code != startupExitRefused || len(c.executed) != 0 || !strings.Contains(out, "Not changed.") {
			t.Fatalf("answer %q: exit %d executed %v\n%s", answer, code, c.executed, out)
		}
	}
}

func TestStartupSystemYesFlagSkipsPrompt(t *testing.T) {
	c := &fakeLaunchdClient{prep: okPrepare(), change: okChange()}
	code, out, _ := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system", "--yes"}, c, false, "")
	if code != startupExitOK || len(c.executed) != 1 || strings.Contains(out, "[y/N]") {
		t.Fatalf("exit %d executed %v\n%s", code, c.executed, out)
	}
}

func TestStartupSystemNoTTYWithoutYesRefuses(t *testing.T) {
	c := &fakeLaunchdClient{prep: okPrepare(), change: okChange()}
	code, _, errOut := runSystemStartup(t, []string{"enable", "com.example.agentd", "--scope", "system"}, c, false, "y\n")
	if code != startupExitRefused || len(c.executed) != 0 || !strings.Contains(errOut, "--yes") {
		t.Fatalf("exit %d executed %v: %s", code, c.executed, errOut)
	}
}

func TestStartupSystemHelperRefusal(t *testing.T) {
	c := &fakeLaunchdClient{prep: helper.LaunchdPrepareResult{Refused: true, Reason: helper.ReasonDenylisted, Message: "matches denylist entry",
		Suggested: []string{"sudo launchctl disable system/com.example.agentd"}}}
	code, out, _ := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system", "--yes"}, c, false, "")
	if code != startupExitRefused || len(c.executed) != 0 || !strings.Contains(out, "sudo launchctl disable") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestStartupSystemMismatchAndErrorExitOne(t *testing.T) {
	for _, res := range []helper.LaunchdChangeResult{
		{Outcome: helper.OutcomeMismatch, Mismatch: "the daemon is still loaded"},
		{Outcome: helper.OutcomeError, Message: "enabled but not loaded"},
	} {
		c := &fakeLaunchdClient{prep: okPrepare(), change: res}
		if code, out, errOut := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system", "--yes"}, c, false, ""); code != startupExitError {
			t.Fatalf("%+v: exit %d\n%s%s", res, code, out, errOut)
		}
	}
	c := &fakeLaunchdClient{prep: okPrepare(), change: helper.LaunchdChangeResult{Refused: true, Reason: helper.ReasonTargetChanged, Message: "run the command again"}}
	if code, _, _ := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system", "--yes"}, c, false, ""); code != startupExitRefused {
		t.Fatalf("target_changed exit %d", code)
	}
}

func TestStartupSystemRateLimitedExitOne(t *testing.T) {
	c := &fakeLaunchdClient{prepErr: &helperclient.RPCError{Code: -32001, Message: "too many"}}
	code, _, errOut := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system"}, c, true, "")
	if code != startupExitError || !strings.Contains(errOut, "too many changes") {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestStartupSystemJSONKeepsStdoutOneObject(t *testing.T) {
	c := &fakeLaunchdClient{prep: okPrepare(), change: okChange()}
	code, out, errOut := runSystemStartup(t, []string{"disable", "com.example.agentd", "--scope", "system", "--yes", "--json"}, c, false, "")
	var res helper.LaunchdChangeResult
	if code != startupExitOK || json.Unmarshal([]byte(out), &res) != nil || res.Via != "spectra-helper" || !strings.Contains(errOut, "Will run:") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
}

func TestStartupSystemWithoutHelperPrintsSudoCommands(t *testing.T) {
	opts, f := startupTestOpts(t)
	dir := t.TempDir()
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>com.example.agentd</string>` +
		`<key>Program</key><string>/opt/d</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "com.example.agentd.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.PlistDirs = append(opts.PlistDirs, startup.PlistDir{Path: dir, Scope: startup.ScopeSystem, Kind: startup.KindLaunchDaemon})
	c := &fakeLaunchdClient{prepErr: helperclient.ErrHelperUnavailable}
	var out, errOut bytes.Buffer
	sys := startupSystem{client: func() launchdHelperClient { return c }, stdin: strings.NewReader(""), isTTY: func() bool { return true }}
	code := runStartupWith([]string{"disable", "com.example.agentd", "--scope", "system"}, &out, &errOut, opts, sys)
	if code != startupExitRefused {
		t.Fatalf("exit %d: %s %s", code, out.String(), errOut.String())
	}
	for _, want := range []string{"sudo launchctl disable system/com.example.agentd", "sudo launchctl bootout system/com.example.agentd", "sudo spectra install-helper"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q:\n%s", want, out.String())
		}
	}
	for _, call := range f.Calls() {
		if call.Args[0] != "print" && call.Args[0] != "print-disabled" {
			t.Fatalf("fallback ran %v", call)
		}
	}
}

func TestStartupUserScopeIgnoresHelper(t *testing.T) {
	c := &fakeLaunchdClient{}
	code, _, errOut := runSystemStartup(t, []string{"disable", "com.example.agent"}, c, true, "")
	if code != startupExitOK || len(c.prepared) != 0 {
		t.Fatalf("exit %d prepared %v: %s", code, c.prepared, errOut)
	}
}
