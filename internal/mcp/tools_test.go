package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
)

func TestToolDefinitionsExposeWorkflowSurface(t *testing.T) {
	got := map[string]bool{}
	for _, def := range toolDefinitions() {
		got[def.Name] = true
	}
	for _, name := range []string{
		"triage",
		"inspect_app",
		"snapshot",
		"diagnose",
		"process",
		"jvm",
		"network",
		"toolchain",
		"issues",
		"remote",
	} {
		if !got[name] {
			t.Fatalf("missing tool definition %q", name)
		}
	}
}

func TestInspectAppRequiresPaths(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	result := s.toolInspectApp(json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected inspect_app without paths to fail")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "paths is required") {
		t.Fatalf("unexpected error content: %+v", result.Content)
	}
}

func TestRemoteHealthDefaultsToTCP(t *testing.T) {
	schema := operationToolDef("remote", "remote", []string{"health"})
	raw, err := json.Marshal(schema.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "operation") {
		t.Fatalf("remote schema does not expose operation: %s", raw)
	}
}

func TestInspectAppDeepUsesInjectedCollectors(t *testing.T) {
	appPath := "/Applications/Codex.app"
	apps := &fakeAppInspector{
		result: detect.Result{
			Path:               appPath,
			UI:                 "Electron",
			Runtime:            "Node+Chromium",
			Language:           "TypeScript/JS",
			Confidence:         "high",
			BundleID:           "com.openai.codex",
			AppVersion:         "26.429.61741",
			TeamID:             "2DC432GLL2",
			HardenedRuntime:    true,
			Entitlements:       []string{"network.client", "cs.allow-jit"},
			GrantedPermissions: []string{"AppleEvents"},
			GatekeeperStatus:   "accepted",
		},
	}
	processes := &fakeProcessCollector{
		procs: []process.Info{
			{PID: 100, PPID: 1, Command: "Codex", AppPath: appPath, RSSKiB: 1024, CPUPct: 2.5, ThreadCount: 10, OpenFDs: 33},
			{PID: 200, PPID: 1, Command: "Other", AppPath: "/Applications/Other.app", RSSKiB: 2048},
		},
	}
	network := &fakeNetworkCollector{
		state: netstate.State{ListeningPorts: []netstate.ListeningPort{{
			PID: 100, Port: 47321, Proto: "tcp", LocalAddr: "127.0.0.1:47321", AppPath: appPath,
		}}},
		conns: []netstate.Connection{{
			PID: 100, Command: "Codex", Proto: "tcp", LocalAddr: "127.0.0.1:53000", RemoteAddr: "203.0.113.10:443", State: "established",
		}},
	}

	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Apps:      apps,
		Processes: processes,
		Network:   network,
		Clock:     fixedClock{t: time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)},
	})

	result := s.toolInspectApp(json.RawMessage(`{"paths":["/Applications/Codex.app"],"network":true,"deep":true}`))
	if result.IsError {
		t.Fatalf("inspect_app returned error: %+v", result.Content)
	}
	if !apps.sawNetwork {
		t.Fatal("expected app inspector to receive ScanNetwork=true")
	}
	if !processes.sawDeep {
		t.Fatal("expected process collector to receive Deep=true")
	}
	if got := strings.Join(processes.sawBundles, ","); got != appPath {
		t.Fatalf("unexpected process bundle attribution paths: %q", got)
	}
	text := result.Content[0].Text
	for _, want := range []string{
		`"deep"`,
		`"open_fds": 33`,
		`"port": 47321`,
		`"remote_addr": "203.0.113.10:443"`,
		`"team_id": "2DC432GLL2"`,
		`"granted_permissions": [`,
		`live: processes=1 rss=1024KiB cpu=2.5 open_fds=33 listening=1 connections=1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("inspect_app deep output missing %q:\n%s", want, text)
		}
	}
}

type fakeAppInspector struct {
	result     detect.Result
	err        error
	sawNetwork bool
}

func (f *fakeAppInspector) InspectApp(_ string, opts detect.Options) (detect.Result, error) {
	f.sawNetwork = opts.ScanNetwork
	return f.result, f.err
}

type fakeProcessCollector struct {
	procs      []process.Info
	sawDeep    bool
	sawBundles []string
}

func (f *fakeProcessCollector) CollectProcesses(_ context.Context, opts process.CollectOptions) []process.Info {
	f.sawDeep = opts.Deep
	f.sawBundles = append([]string{}, opts.BundlePaths...)
	return append([]process.Info{}, f.procs...)
}

func (f *fakeProcessCollector) SampleProcess(_, _, _ int) (string, error) {
	return "sample", nil
}

type fakeNetworkCollector struct {
	state netstate.State
	conns []netstate.Connection
}

func (f fakeNetworkCollector) CollectNetworkState() netstate.State {
	return f.state
}

func (f fakeNetworkCollector) CollectConnections() []netstate.Connection {
	return append([]netstate.Connection{}, f.conns...)
}

type fixedClock struct {
	t time.Time
}

func (f fixedClock) Now() time.Time {
	return f.t
}
