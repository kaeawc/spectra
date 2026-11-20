package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/startup"
)

// testLaunchd models one loaded, enabled agent in gui/501. With stuck set,
// disable and bootout exit 0 but change nothing.
type testLaunchd struct {
	mu               sync.Mutex
	calls            []proc.Call
	disabled, loaded bool
	stuck            bool
}

func (l *testLaunchd) Run(_ context.Context, c proc.Cmd) (proc.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, proc.Call{Name: c.Name, Args: append([]string{}, c.Args...)})
	if c.Name != "/bin/launchctl" || len(c.Args) < 2 {
		return proc.Result{ExitCode: -1}, fmt.Errorf("unexpected %s %v", c.Name, c.Args)
	}
	out := ""
	switch c.Args[0] {
	case "print-disabled":
		out = "disabled services = {\n}\n"
		if c.Args[1] == "gui/501" {
			state := map[bool]string{true: "disabled", false: "enabled"}[l.disabled]
			out = "disabled services = {\n\t\"com.example.agent\" => " + state + "\n}\n"
		}
	case "print":
		out = c.Args[1] + " = {\n\tservices = {\n"
		if c.Args[1] == "gui/501" && l.loaded {
			out += "\t\t      55      - \tcom.example.agent\n"
		}
		out += "\t}\n}\n"
	case "disable", "bootout":
		if !l.stuck {
			l.disabled = l.disabled || c.Args[0] == "disable"
			l.loaded = l.loaded && c.Args[0] != "bootout"
		}
	default:
		return proc.Result{ExitCode: -1}, fmt.Errorf("unexpected launchctl %v", c.Args)
	}
	return proc.Result{Stdout: []byte(out)}, nil
}

func (l *testLaunchd) Calls() []proc.Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]proc.Call(nil), l.calls...)
}

func (l *testLaunchd) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = nil
}

func startupTestOpts(t *testing.T) (startup.Options, *testLaunchd) {
	t.Helper()
	dir := t.TempDir()
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>com.example.agent</string>` +
		`<key>Program</key><string>/opt/a</string><key>RunAtLoad</key><true/></dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "com.example.agent.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &testLaunchd{loaded: true}
	return startup.Options{
		OS:         hostos.Darwin,
		Runner:     f,
		EUID:       func() int { return 501 },
		Getenv:     func(string) string { return "" },
		LookupHome: func(int) (string, error) { return dir, nil },
		LookupUser: func(uint32) string { return "tester" },
		PlistDirs:  []startup.PlistDir{{Path: dir, Scope: startup.ScopeUser, Kind: startup.KindLaunchAgent}},
		AppDirs:    []string{},
		// An empty store directory, so tests never read the host's BTM store.
		BTMStoreDir: t.TempDir(),
	}, f
}

func TestStartupListJSON(t *testing.T) {
	opts, _ := startupTestOpts(t)
	var out, errOut bytes.Buffer
	if code := runStartupWithIO([]string{"list", "--json", "--scope", "user"}, &out, &errOut, opts); code != startupExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var rep startup.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if len(rep.Items) != 1 || rep.Items[0].Label != "com.example.agent" || rep.Items[0].PID != 55 || rep.Items[0].Trigger != "login" {
		t.Fatalf("items = %+v", rep.Items)
	}
	if len(rep.Sources) == 0 {
		t.Fatal("no sources")
	}
}

func TestStartupListTableIsDefault(t *testing.T) {
	opts, _ := startupTestOpts(t)
	var out, errOut bytes.Buffer
	if code := runStartupWithIO(nil, &out, &errOut, opts); code != startupExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"Startup items (1)", "com.example.agent", "enabled,running", "needs_root"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("table missing %q:\n%s", want, out.String())
		}
	}
}

func TestStartupUsage(t *testing.T) {
	opts, f := startupTestOpts(t)
	for _, args := range [][]string{{"bogus"}, {"list", "--scope", "everyone"}, {"disable"}, {"enable", "a", "b"}} {
		var out, errOut bytes.Buffer
		if code := runStartupWithIO(args, &out, &errOut, opts); code != startupExitUsage {
			t.Fatalf("%v: exit %d, want usage", args, code)
		}
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("usage errors ran commands: %v", f.Calls())
	}
}

func TestStartupDisableJSONAndRefusal(t *testing.T) {
	opts, f := startupTestOpts(t)
	var out, errOut bytes.Buffer
	if code := runStartupWithIO([]string{"disable", "com.example.agent", "--json"}, &out, &errOut, opts); code != startupExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var res startup.ChangeResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, out.String())
	}
	if res.Action != "disable" || len(res.Commands) != 2 || res.After == nil {
		t.Fatalf("res = %+v", res)
	}
	var mutating []string
	for _, c := range f.Calls() {
		if c.Args[0] == "disable" || c.Args[0] == "bootout" {
			mutating = append(mutating, strings.Join(c.Args, " "))
		}
	}
	if strings.Join(mutating, ";") != "disable gui/501/com.example.agent;bootout gui/501/com.example.agent" {
		t.Fatalf("mutating = %v", mutating)
	}

	f.Reset()
	out.Reset()
	if code := runStartupWithIO([]string{"enable", "com.apple.example.agent"}, &out, &errOut, opts); code != startupExitRefused {
		t.Fatalf("apple enable exit %d", code)
	}
	if !strings.Contains(out.String(), "Not changed") {
		t.Fatalf("output = %s", out.String())
	}
	for _, c := range f.Calls() {
		if c.Args[0] != "print" && c.Args[0] != "print-disabled" {
			t.Fatalf("refusal ran %v", c)
		}
	}
}

func TestStartupChangeExitsNonZeroWhenStateDoesNotMatch(t *testing.T) {
	opts, f := startupTestOpts(t)
	f.stuck = true
	var out, errOut bytes.Buffer
	if code := runStartupWithIO([]string{"disable", "com.example.agent"}, &out, &errOut, opts); code != startupExitError {
		t.Fatalf("exit %d, want %d when launchctl changed nothing:\n%s", code, startupExitError, out.String())
	}
	if !strings.Contains(errOut.String(), "did not take effect") {
		t.Fatalf("stderr = %s", errOut.String())
	}
}
