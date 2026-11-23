package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kaeawc/spectra/internal/corefile"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/playbook"
	"github.com/kaeawc/spectra/internal/snapshot"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeJVMs struct{ JVMCollector }

func (fakeJVMs) CollectJVMs(context.Context, jvm.CollectOptions) []jvm.Info {
	return []jvm.Info{{PID: 20, MainClass: "svc.App"}}
}

func (fakeJVMs) InspectJVM(_ context.Context, pid int, _ jvm.CollectOptions) *jvm.Info {
	return &jvm.Info{PID: pid, MainClass: "svc.App"}
}

type fakeCoreFiles struct{}

func (fakeCoreFiles) Inspect(context.Context, string, string) (corefile.Report, error) {
	return corefile.Report{Runtime: "go"}, nil
}

type fakeWatch struct{}

func (fakeWatch) Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error) {
	return hostwatch.Sample{NCPU: 8, Load1: 1}, []hostwatch.Alert{{ID: "a1", State: "firing"}}, nil
}
func (fakeWatch) Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error) {
	return []hostwatch.Sample{{NCPU: 8}}, nil
}
func (fakeWatch) Alerts(context.Context, string, int) ([]hostwatch.Alert, error) {
	return []hostwatch.Alert{{ID: "a1", State: "firing"}}, nil
}
func (fakeWatch) Ack(context.Context, string) (hostwatch.Alert, error) {
	return hostwatch.Alert{ID: "a1"}, nil
}
func (fakeWatch) ProcessHistory(context.Context, int, int) ([]metrics.Sample, error) {
	return []metrics.Sample{{PID: 20}}, nil
}
func (fakeWatch) Close() error { return nil }

// structuredTestServer fakes every host seam, so each tool answers without
// touching the host; the local store and cache live under a temp HOME.
func structuredTestServer(t *testing.T) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	asked := 0
	s := reportTestServer(t, &asked)
	c := s.collect
	c.Apps = &fakeAppInspector{result: detect.Result{Path: "/Applications/X.app", UI: "AppKit"}}
	c.Snapshots = fakeSnapshotCollector{snap: snapshot.Snapshot{ID: "snap-1"}}
	c.JVMs = fakeJVMs{}
	c.Toolchain = emptyToolchainCollector{}
	c.Network = fakeNetworkCollector{conns: []netstate.Connection{{PID: 20, Proto: "tcp", LocalAddr: "127.0.0.1:1", RemoteAddr: "10.0.0.1:443"}}}
	c.DB = &fakeDBInspector{}
	c.Power = &fakePowerCollector{}
	c.Memory = fakeMemoryCollector{}
	c.Storage = &fakeStorageCollector{}
	c.System = &fakeSystemCollector{}
	c.Services = fakeServicesCollector{}
	c.Logs = &fakeLogCollector{}
	c.Updates = fakeUpdatesCollector{}
	c.TimeMachine = fakeTimeMachineCollector{}
	c.CoreFiles = fakeCoreFiles{}
	c.Playbooks = fakePlaybookCatalog{list: []playbook.Playbook{{ID: "p1", Title: "P"}}}
	c.Reports.Orphans = func() orphans.ReportDeps { return fakeOrphansReportDeps(&asked) }
	c.Reports.Find = func(_ context.Context, tg owner.Target) (owner.Result, error) { return owner.Result{Target: tg}, nil }
	c.Clock = fixedClock{t: reportNow}
	s.collect = c
	s.SetDaemonConnector(func(context.Context) (daemonWatchClient, bool) { return fakeWatch{}, true })
	return s
}

// structuredCalls is one successful call per tool.
var structuredCalls = map[string]string{
	"triage":      `{"pid":20}`,
	"inspect_app": `{"paths":["/Applications/X.app"],"include_raw":true}`,
	"snapshot":    `{"operation":"create","include_raw":true}`,
	"diagnose":    `{"include_raw":true}`,
	"process":     `{"operation":"list","include_raw":true}`,
	"jvm":         `{"operation":"list","include_raw":true}`,
	"network":     `{"operation":"state","include_raw":true}`,
	"db":          `{"operation":"discover"}`,
	"toolchain":   `{"operation":"scan","include_raw":true}`,
	"issues":      `{"operation":"list","machine_uuid":"m1"}`,
	"power":       `{}`,
	"memory":      `{}`,
	"storage":     `{}`,
	"system":      `{}`,
	"services":    `{}`,
	"logs":        `{"process":"backupd"}`,
	"updates":     `{}`,
	"timemachine": `{}`,
	"playbook":    `{"operation":"list"}`,
	"cache":       `{"operation":"stats"}`,
	"core":        `{"path":"/tmp/core.1"}`,
	"metrics":     `{"operation":"process"}`,
	"host_health": `{"samples":1}`,
	"alerts":      `{}`,
	"load":        `{"roots":["/r"]}`,
	"orphans":     `{"roots":["/work"]}`,
	"lineage":     `{"pid":20}`,
	"owner":       `{"target":":8080"}`,
	"spawns":      `{"duration_seconds":3,"interval_millis":1000}`,
	"idle":        `{"pids":[20],"min_age":"1m","window":"100ms"}`,
}

// TestStructuredContentValidatesForEveryTool runs every tool over the wire
// and checks that structuredContent validates against the listed
// outputSchema, carries the same value as the text content, and that the
// text is exactly what the handler renders.
func TestStructuredContentValidatesForEveryTool(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)() // exercises the macOS paths with fakes
	srv := structuredTestServer(t)
	cs := startServerSession(t, srv, "2025-11-25", nil)
	ctx := context.Background()
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(structuredCalls) + len(jobToolDefinitions()); len(list.Tools) != want {
		t.Errorf("%d tools listed, want %d (%d covered here, job tools in jobs_test.go)", len(list.Tools), want, len(structuredCalls))
	}
	for _, tool := range list.Tools {
		if _, isJobTool := jobToolReadOnly[tool.Name]; isJobTool {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			args, ok := structuredCalls[tool.Name]
			if !ok {
				t.Fatalf("no structured-output case for %s", tool.Name)
			}
			resolved := resolveOutputSchema(t, tool.OutputSchema)
			res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: tool.Name, Arguments: json.RawMessage(args)})
			if err != nil {
				t.Fatal(err)
			}
			text := res.Content[0].(*sdk.TextContent).Text
			if res.IsError {
				t.Fatalf("isError: %s", text)
			}
			if res.StructuredContent == nil {
				t.Fatal("no structuredContent")
			}
			if err := resolved.Validate(res.StructuredContent); err != nil {
				t.Fatalf("structuredContent does not validate: %v\n%s", err, text)
			}
			var fromText any
			if err := json.Unmarshal([]byte(text), &fromText); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fromText, res.StructuredContent) {
				t.Fatalf("structuredContent differs from the text content")
			}
			direct := srv.toolHandlers()[tool.Name](ctx, json.RawMessage(args))
			if direct.Content[0].Text != text {
				t.Fatalf("wire text differs from the handler's text:\nwire %s\nhandler %s", text, direct.Content[0].Text)
			}
		})
	}
}

func resolveOutputSchema(t *testing.T, schema any) *jsonschema.Resolved {
	t.Helper()
	if schema == nil {
		t.Fatal("no outputSchema")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" {
		t.Fatalf("outputSchema type %q, want object", s.Type)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve outputSchema: %v", err)
	}
	return resolved
}

func TestErrorResultsCarryNoStructuredContent(t *testing.T) {
	cs := startServerSession(t, structuredTestServer(t), "2025-11-25", nil)
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "lineage", Arguments: map[string]any{}})
	if err != nil || !res.IsError || res.StructuredContent != nil {
		t.Fatalf("error result = %+v, %v; want isError without structuredContent", res, err)
	}
}

func TestEnvelopeSchemaRejectsWrongShape(t *testing.T) {
	resolved := resolveOutputSchema(t, outputSchema("memory"))
	if err := resolved.Validate(map[string]any{"summary": 1, "timestamp": "x"}); err == nil {
		t.Fatal("a numeric summary validated against the envelope schema")
	}
	if err := resolved.Validate(map[string]any{"summary": "s", "timestamp": "x", "extra": true}); err == nil {
		t.Fatal("an unknown envelope field validated")
	}
}

func TestOutputSchemaFallsBackForRecursiveTypes(t *testing.T) {
	type node struct {
		Children []node `json:"children"`
	}
	s := schemaForType(reflect.TypeFor[node]())
	if s.Type != "object" || s.Properties != nil {
		t.Fatalf("recursive type schema = %+v, want the permissive object schema", s)
	}
}
