package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestWithAgentAttachedSkipsAutoAttachWhenDisabled(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	disabled := false
	calls := 0
	res, ok := s.withAgentAttached(jvmParams{PID: 4321, AutoAttach: &disabled}, func() error {
		calls++
		return &jvm.NotAttachedError{PID: 4321}
	})
	if ok {
		t.Fatal("expected withAgentAttached to report failure")
	}
	if calls != 1 {
		t.Fatalf("expected one op call, got %d", calls)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("expected structured error result, got %+v", res)
	}
	var payload struct {
		Error       string `json:"error"`
		PID         int    `json:"pid"`
		Message     string `json:"message"`
		Remediation struct {
			Tool      string `json:"tool"`
			Operation string `json:"operation"`
			PID       int    `json:"pid"`
		} `json:"remediation"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &payload); err != nil {
		t.Fatalf("decode payload: %v\n%s", err, res.Content[0].Text)
	}
	if payload.Error != "agent_not_attached" || payload.PID != 4321 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Remediation.Tool != "jvm" || payload.Remediation.Operation != "attach" || payload.Remediation.PID != 4321 {
		t.Fatalf("unexpected remediation: %+v", payload.Remediation)
	}
	if !strings.Contains(payload.Message, "auto_attach is disabled") {
		t.Fatalf("expected disabled message, got %q", payload.Message)
	}
}

func TestWithAgentAttachedPassesThroughOtherErrors(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	disabled := false
	res, ok := s.withAgentAttached(jvmParams{PID: 1, AutoAttach: &disabled}, func() error {
		return errors.New("boom")
	})
	if ok {
		t.Fatal("expected failure for non-NotAttached error")
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "boom") {
		t.Fatalf("expected plain error, got %+v", res)
	}
}

func TestWithAgentAttachedReturnsSuccess(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	res, ok := s.withAgentAttached(jvmParams{PID: 1}, func() error { return nil })
	if !ok {
		t.Fatalf("expected success, got %+v", res)
	}
}

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

func TestJVMToolSchemaExposesMBeanProperties(t *testing.T) {
	var jvm ToolDefinition
	for _, def := range toolDefinitions() {
		if def.Name == "jvm" {
			jvm = def
			break
		}
	}
	if jvm.Name == "" {
		t.Fatal("jvm tool definition missing")
	}
	raw, err := json.Marshal(jvm.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	schema := string(raw)
	for _, want := range []string{
		`"mbean_name"`,
		`"attribute"`,
		`"mbean_operation"`,
		`"asprof_path"`,
		`"agent"`,
		`"samples"`,
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("jvm input schema missing %s:\n%s", want, schema)
		}
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

func TestTriageScopedToTargetFiltersFindings(t *testing.T) {
	slackPath := "/Applications/Slack.app"
	apps := []detect.Result{
		{Path: slackPath, BundleID: "com.tinyspeck.slackmacgap", TeamID: "BQR82RBBHL", HardenedRuntime: true, GatekeeperStatus: "accepted"},
		{Path: "/Applications/Arbigent.app", BundleID: "com.arbigent", TeamID: "", GatekeeperStatus: "rejected"},
		{Path: "/Applications/Xcode-26.3.0.app", BundleID: "com.apple.dt.Xcode", TeamID: "APPLECORP", HardenedRuntime: false},
	}
	procs := []process.Info{
		{PID: 96454, PPID: 1, Command: "Slack", AppPath: slackPath, RSSKiB: 4096},
	}

	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Processes: &fakeProcessCollector{procs: procs},
		Snapshots: fakeSnapshotCollector{snap: snapshot.Snapshot{Apps: apps}},
		Clock:     fixedClock{t: time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)},
	})

	result := s.toolTriage(json.RawMessage(`{"pid":96454,"include_raw":true}`))
	if result.IsError {
		t.Fatalf("triage returned error: %+v", result.Content)
	}
	text := result.Content[0].Text
	for _, bad := range []string{"Arbigent", "Xcode-26.3.0"} {
		if strings.Contains(text, bad) {
			t.Fatalf("triage pid=96454 leaked unrelated finding mentioning %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "no rules matched") {
		t.Fatalf("expected explicit \"no rules matched\" message when target has no findings:\n%s", text)
	}
}

func TestTriageScopedKeepsMatchingFindings(t *testing.T) {
	slackPath := "/Applications/Slack.app"
	apps := []detect.Result{
		{Path: slackPath, BundleID: "com.tinyspeck.slackmacgap", TeamID: ""},
		{Path: "/Applications/Arbigent.app", BundleID: "com.arbigent", TeamID: ""},
	}

	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Snapshots: fakeSnapshotCollector{snap: snapshot.Snapshot{Apps: apps}},
		Clock:     fixedClock{t: time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)},
	})

	result := s.toolTriage(json.RawMessage(`{"app_path":"/Applications/Slack.app","include_raw":true}`))
	if result.IsError {
		t.Fatalf("triage returned error: %+v", result.Content)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "Slack") {
		t.Fatalf("expected Slack finding to be present:\n%s", text)
	}
	if strings.Contains(text, "Arbigent") {
		t.Fatalf("Arbigent must not appear in target-scoped triage:\n%s", text)
	}
}

func TestTriageScopeGlobalRestoresHostWideFindings(t *testing.T) {
	apps := []detect.Result{
		{Path: "/Applications/Slack.app", BundleID: "com.tinyspeck.slackmacgap", TeamID: "BQR82RBBHL", HardenedRuntime: true, GatekeeperStatus: "accepted"},
		{Path: "/Applications/Arbigent.app", BundleID: "com.arbigent", TeamID: ""},
	}
	procs := []process.Info{{PID: 1, Command: "Slack", AppPath: "/Applications/Slack.app"}}

	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Processes: &fakeProcessCollector{procs: procs},
		Snapshots: fakeSnapshotCollector{snap: snapshot.Snapshot{Apps: apps}},
		Clock:     fixedClock{t: time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)},
	})

	result := s.toolTriage(json.RawMessage(`{"pid":1,"scope":"global","include_raw":true}`))
	if result.IsError {
		t.Fatalf("triage returned error: %+v", result.Content)
	}
	if !strings.Contains(result.Content[0].Text, "Arbigent") {
		t.Fatalf("scope=global must include host-wide findings; got:\n%s", result.Content[0].Text)
	}
}

type fakeSnapshotCollector struct {
	snap snapshot.Snapshot
}

func (f fakeSnapshotCollector) BuildSnapshot(_ context.Context, _ snapshot.Options) snapshot.Snapshot {
	return f.snap
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
