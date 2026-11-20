package startup

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/proc"
)

func changeFixture(t *testing.T) (tree, *fakeLaunchd, string) {
	t.Helper()
	tr := newTree(t)
	plist := writeJob(t, tr.userAgents, "com.example.agent", `<key>Program</key><string>/opt/a</string><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.userAgents, "com.example.ondemand", `<key>Program</key><string>/opt/o</string>`)
	writeJob(t, tr.userAgents, "com.apple.user.thing", `<key>Program</key><string>/opt/x</string>`)
	writeJob(t, tr.libDaemons, "com.example.daemon", `<key>Program</key><string>/opt/d</string><key>RunAtLoad</key><true/>`)
	writeJob(t, tr.libAgents, "com.example.global", `<key>Program</key><string>/opt/g</string><key>RunAtLoad</key><true/>`)
	f := newFakeLaunchd()
	f.plists[plist] = "com.example.agent"
	return tr, f, plist
}

func TestDisableLoadedRunsDisableThenBootout(t *testing.T) {
	tr, f, _ := changeFixture(t)
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"disable gui/501/com.example.agent", "bootout gui/501/com.example.agent"}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating calls = %v, want %v", got, want)
	}
	if res.Refused || len(res.Commands) != 2 || res.Commands[0].Argv[0] != launchctlPath {
		t.Fatalf("res = %+v", res)
	}
	if res.Before == nil || !res.Before.Running || res.After == nil || !res.After.Disabled || res.After.Loaded {
		t.Fatalf("before %+v after %+v", res.Before, res.After)
	}
}

func TestDisableNotLoadedSkipsBootout(t *testing.T) {
	tr, f, _ := changeFixture(t)
	if _, err := Disable(context.Background(), tr.opts(f), "com.example.agent", ""); err != nil {
		t.Fatal(err)
	}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, []string{"disable gui/501/com.example.agent"}) {
		t.Fatalf("mutating calls = %v", got)
	}
}

func TestDisableReportsLaunchctlFailure(t *testing.T) {
	tr, f, _ := changeFixture(t)
	f.fail["disable"] = true
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err == nil || !strings.Contains(err.Error(), "exited 5") {
		t.Fatalf("err = %v", err)
	}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, []string{"disable gui/501/com.example.agent"}) {
		t.Fatalf("bootout must not follow a failed disable: %v", got)
	}
	if len(res.Commands) != 1 || res.Commands[0].ExitCode != 5 || res.Commands[0].Stderr == "" {
		t.Fatalf("commands = %+v", res.Commands)
	}
}

func TestEnableBootstrapsRunAtLoadWhenNotLoaded(t *testing.T) {
	tr, f, plist := changeFixture(t)
	f.disabled["gui/501"]["com.example.agent"] = true
	res, err := Enable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"enable gui/501/com.example.agent", "bootstrap gui/501 " + plist}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating calls = %v, want %v", got, want)
	}
	if res.After == nil || res.After.Disabled || !res.After.Running || res.After.PID != 4242 {
		t.Fatalf("after = %+v", res.After)
	}
}

func TestEnableBootstrapsWheneverNotLoaded(t *testing.T) {
	tr, f, _ := changeFixture(t)
	f.loaded["gui/501"]["com.example.agent"] = serviceState{}
	if _, err := Enable(context.Background(), tr.opts(f), "com.example.agent", "user"); err != nil {
		t.Fatal(err)
	}
	// Not RunAtLoad: bootstrap still loads it (without starting it), which
	// is what a disable's bootout undid.
	res, err := Enable(context.Background(), tr.opts(f), "com.example.ondemand", "user")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"enable gui/501/com.example.agent", "enable gui/501/com.example.ondemand",
		"bootstrap gui/501 " + filepath.Join(tr.userAgents, "com.example.ondemand.plist")}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating calls = %v, want %v", got, want)
	}
	if res.After == nil || !res.After.Loaded || res.Mismatch != "" {
		t.Fatalf("after = %+v mismatch %q", res.After, res.Mismatch)
	}
}

func TestEnableUndoesDisableForIntervalJob(t *testing.T) {
	tr, f, _ := changeFixture(t)
	writeJob(t, tr.userAgents, "com.example.interval", `<key>Program</key><string>/opt/i</string><key>StartInterval</key><integer>600</integer>`)
	f.loaded["gui/501"]["com.example.interval"] = serviceState{}
	opts := tr.opts(f)
	if res, err := Disable(context.Background(), opts, "com.example.interval", "user"); err != nil || res.After.Loaded || res.Mismatch != "" {
		t.Fatalf("disable = %+v, %v", res, err)
	}
	res, err := Enable(context.Background(), opts, "com.example.interval", "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.After == nil || res.After.Disabled || !res.After.Loaded || res.Mismatch != "" {
		t.Fatalf("enable left the job unloaded: after %+v mismatch %q", res.After, res.Mismatch)
	}
}

func TestDisableRereadsStateAfterFailure(t *testing.T) {
	tr, f, _ := changeFixture(t)
	f.fail["bootout"] = true
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err == nil {
		t.Fatal("want bootout error")
	}
	if res.After == nil || !res.After.Disabled || !res.After.Loaded || res.Mismatch == "" {
		t.Fatalf("half-done disable should report current state: after %+v mismatch %q", res.After, res.Mismatch)
	}
}

func TestChangeRefusesWhenLaunchdStateIsUnreadable(t *testing.T) {
	for _, verb := range []string{"print", "print-disabled"} {
		t.Run(verb, func(t *testing.T) {
			tr, f, _ := changeFixture(t)
			f.fail[verb] = true
			f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
			_, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
			if err == nil || !strings.Contains(err.Error(), "can't read launchd state") {
				t.Fatalf("err = %v", err)
			}
			for _, c := range f.Calls() {
				if len(c.Args) > 0 && (c.Args[0] == "disable" || c.Args[0] == "bootout") {
					t.Fatalf("ran %v without launchd state", c.Args)
				}
			}
		})
	}
}

func TestDisableBootoutRace(t *testing.T) {
	cases := []struct {
		name    string
		result  proc.Result
		pending bool
	}{
		{name: "no such process", result: proc.Result{ExitCode: 3, Stderr: []byte("Boot-out failed: 3: No such process")}},
		{name: "not found", result: proc.Result{ExitCode: 113, Stderr: []byte("Boot-out failed: 113: Could not find specified service")}},
		{name: "in progress", result: proc.Result{ExitCode: 36, Stderr: []byte("Boot-out failed: 36: Operation now in progress")}, pending: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, f, _ := changeFixture(t)
			f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
			f.result["bootout"] = tc.result
			f.keepLoaded = tc.pending // launchd is still stopping it
			res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
			if err != nil {
				t.Fatal(err)
			}
			if res.Pending != tc.pending || res.Message == "" || res.Mismatch != "" {
				t.Fatalf("res = %+v", res)
			}
			if tc.pending && !strings.Contains(res.Message, "in progress") {
				t.Fatalf("message = %q", res.Message)
			}
		})
	}
}

func TestDisableRefusesDuplicateLabels(t *testing.T) {
	tr, f, _ := changeFixture(t)
	writeJob(t, tr.libAgents, "com.example.agent", `<key>Program</key><string>/opt/other</string>`)
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason != RefuseDuplicate || !strings.Contains(res.Message, tr.userAgents) || !strings.Contains(res.Message, tr.libAgents) {
		t.Fatalf("res = %+v", res)
	}
	if m := mutatingCalls(f.Calls()); len(m) != 0 {
		t.Fatalf("ran %v", m)
	}
}

func TestChangeRefusesLabelUnderSystem(t *testing.T) {
	tr, f, _ := changeFixture(t)
	sys := filepath.Join(tr.root, "System", "Library", "LaunchAgents")
	writeJob(t, sys, "com.example.sshagent", `<key>Program</key><string>/usr/bin/agent</string>`)
	opts := tr.opts(f)
	opts.PlistDirs = append(opts.PlistDirs, PlistDir{Path: sys, Scope: ScopeSystem, Kind: KindLaunchAgent, Apple: true})
	for _, withUserCopy := range []bool{false, true} {
		if withUserCopy {
			writeJob(t, tr.userAgents, "com.example.sshagent", `<key>Program</key><string>/usr/bin/agent</string><key>RunAtLoad</key><true/>`)
		}
		// IncludeApple is not set: disable must read /System anyway.
		res, err := Disable(context.Background(), opts, "com.example.sshagent", "user")
		if err != nil {
			t.Fatal(err)
		}
		if res.Reason != RefuseAppleDir || !strings.Contains(res.Message, sys) {
			t.Fatalf("user copy %v: res = %+v", withUserCopy, res)
		}
	}
	if m := mutatingCalls(f.Calls()); len(m) != 0 {
		t.Fatalf("ran %v", m)
	}
}

func TestChangeUnderSudoActsOnInvokingUser(t *testing.T) {
	tr, f, plist := changeFixture(t)
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 12}
	opts := tr.opts(f)
	opts.EUID = func() int { return 0 }
	opts.Getenv = func(k string) string {
		if k == "SUDO_UID" {
			return "501"
		}
		return ""
	}
	res, err := Disable(context.Background(), opts, "com.example.agent", "user")
	if err != nil || res.Mismatch != "" {
		t.Fatalf("disable = %+v, %v", res, err)
	}
	if _, err := Enable(context.Background(), opts, "com.example.agent", "user"); err != nil {
		t.Fatal(err)
	}
	want := []string{"disable gui/501/com.example.agent", "bootout gui/501/com.example.agent",
		"enable gui/501/com.example.agent", "bootstrap gui/501 " + plist}
	if got := mutatingCalls(f.Calls()); !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating calls = %v, want %v", got, want)
	}
	sfl := 0
	for _, c := range f.Calls() {
		if c.Name == sfltoolPath {
			sfl++
		}
	}
	if sfl != 2 {
		t.Fatalf("sfltool ran %d times for two changes, want once per change", sfl)
	}

	res, err = Disable(context.Background(), opts, "com.example.daemon", "user")
	if err != nil || res.Reason != RefuseSystem {
		t.Fatalf("daemon = %+v, %v", res, err)
	}
	if strings.Contains(res.Message, "sudo") || strings.Contains(res.Message, "needs root") || strings.Contains(strings.Join(res.Suggested, ";"), "sudo") ||
		res.Suggested[0] != "launchctl disable system/com.example.daemon" {
		t.Fatalf("already root, so no sudo: %+v", res)
	}
	res, err = Disable(context.Background(), opts, "com.example.missing", "user")
	if err != nil || res.Reason != RefuseUnknown || len(res.Suggested) != 0 {
		t.Fatalf("unknown under root = %+v, %v", res, err)
	}
}

func TestChangeRefusals(t *testing.T) {
	cases := []struct {
		name, action, label, scope, reason, suggest string
		btm                                         bool
	}{
		{name: "apple", action: "disable", label: "com.apple.user.thing", reason: RefuseApple, suggest: "launchctl disable gui/501/com.apple.user.thing"},
		{name: "apple unknown", action: "enable", label: "com.apple.nope", reason: RefuseApple},
		{name: "system daemon", action: "disable", label: "com.example.daemon", reason: RefuseSystem, suggest: "sudo launchctl disable system/com.example.daemon"},
		{name: "system agent", action: "enable", label: "com.example.global", reason: RefuseSystem, suggest: "launchctl enable gui/501/com.example.global"},
		{name: "unknown", action: "disable", label: "com.example.missing", reason: RefuseUnknown,
			suggest: `open "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"`},
		{name: "scope flag", action: "disable", label: "com.example.agent", scope: "system", reason: RefuseScope},
		{name: "btm only", action: "disable", label: "com.example.notes.launcher", reason: RefuseBTMOnly, btm: true,
			suggest: `open "x-apple.systempreferences:com.apple.LoginItems-Settings.extension"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, f, _ := changeFixture(t)
			opts := tr.opts(f)
			if tc.btm {
				f.btm = readBTMFixture(t)
				opts.EUID = func() int { return 0 }
				opts.Getenv = func(string) string { return "501" }
			}
			do := Disable
			if tc.action == "enable" {
				do = Enable
			}
			res, err := do(context.Background(), opts, tc.label, tc.scope)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Refused || res.Reason != tc.reason || res.Message == "" {
				t.Fatalf("res = %+v", res)
			}
			if tc.suggest != "" && (len(res.Suggested) == 0 || res.Suggested[0] != tc.suggest) {
				t.Fatalf("suggested = %v, want first %q", res.Suggested, tc.suggest)
			}
			if m := mutatingCalls(f.Calls()); len(m) != 0 {
				t.Fatalf("refusal ran mutating commands: %v", m)
			}
			if len(res.Commands) != 0 {
				t.Fatalf("commands = %+v", res.Commands)
			}
		})
	}
}

func TestChangeResultJSONShape(t *testing.T) {
	tr, f, _ := changeFixture(t)
	f.loaded["gui/501"]["com.example.agent"] = serviceState{PID: 3}
	res, err := Disable(context.Background(), tr.opts(f), "com.example.agent", "user")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(res)
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"action", "label", "domain", "refused", "commands", "before", "after"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("missing %q in %s", k, data)
		}
	}
	cmd := v["commands"].([]any)[0].(map[string]any)
	if _, ok := cmd["argv"]; !ok {
		t.Fatalf("command shape %v", cmd)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("gui/501/com.x"); got != "gui/501/com.x" {
		t.Fatal(got)
	}
	if got := shellQuote("/Library/My Agents/it's.plist"); got != `'/Library/My Agents/it'\''s.plist'` {
		t.Fatal(got)
	}
}
