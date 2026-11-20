package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawnorigin"
	"github.com/kaeawc/spectra/internal/spawntrace"
)

var reportNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func fakeLoadReportDeps() hostload.ReportDeps {
	files := map[string]string{"/proc/loadavg": "1.00 1 1 1/1 1", "/proc/meminfo": "MemAvailable: 16777216 kB\n"}
	return hostload.ReportDeps{
		Host: hostload.Host{
			OS: hostos.Linux,
			ReadFile: func(p string) ([]byte, error) {
				if s, ok := files[p]; ok {
					return []byte(s), nil
				}
				return nil, errors.New("missing")
			},
			Readlink: func(p string) (string, error) {
				if p == "/proc/10/cwd" {
					return "/r/wt1/sub", nil
				}
				return "", errors.New("denied")
			},
			NumCPU: func() int { return 4 },
		},
		Procs: func(context.Context) []process.Info {
			return []process.Info{{PID: 10, Command: "go", CPUPct: 90, RSSKiB: 1024}, {PID: 11, Command: "mystery", CPUPct: 5}}
		},
		FS:  hostload.FS{Exists: func(p string) bool { return p == "/r/wt1/.git" }},
		Now: func() time.Time { return reportNow },
	}
}

func fakeOrphansReportDeps(asked *int) orphans.ReportDeps {
	old := reportNow.Add(-time.Hour)
	return orphans.ReportDeps{
		Procs: func(context.Context) []process.Info {
			return []process.Info{
				{PID: 1, Command: "launchd"},
				{PID: 10, PPID: 1, UID: 501, User: "me", Command: "node", RSSKiB: 2048, StartTime: old},
				{PID: 11, PPID: 1, UID: 501, User: "me", Command: "gradle", RSSKiB: 4096, StartTime: old},
			}
		},
		Cwds: func(context.Context, []int) map[int]string {
			return map[int]string{10: "/work/wt1/sub", 11: "/work/gone"}
		},
		Stat: func(p string) error {
			if p == "/work/wt1/.git" || p == "/work/wt1/sub" {
				return nil
			}
			return fs.ErrNotExist
		},
		Now:   func() time.Time { return reportNow },
		Getwd: func() (string, error) { return "", errors.New("no cwd default in MCP") },
		Pid:   func() int { return 99 },
		Origins: func(context.Context, []spawnorigin.Query) orphans.OriginSource {
			*asked++
			return orphans.OriginSource{Unavailable: "fake daemon"}
		},
	}
}

type reportDirStat struct{ os.FileInfo }

func (reportDirStat) IsDir() bool { return true }

func fakeIdleReportDeps() idle.ReportDeps {
	procs := []process.Info{
		{PID: 1, Command: "init"},
		{PID: 20, PPID: 1, Command: "slot", StartTime: reportNow.Add(-time.Hour)},
		{PID: 21, PPID: 20, Command: "slot", StartTime: reportNow.Add(-time.Hour)},
	}
	return idle.ReportDeps{
		Sample: func(_ context.Context, w time.Duration) (idle.Sample, error) {
			return idle.Sample{Procs: procs, CPU: map[int]float64{20: 0, 21: 0.1}, Window: w, Basis: idle.BasisWindow}, nil
		},
		Cwds: func(context.Context, []process.Info) (map[int]string, bool, error) {
			return map[int]string{20: "/work/lane", 21: "/work/lane/sub"}, false, nil
		},
		Waits:        func(context.Context, []int) map[int]*idle.Wait { return nil },
		Stat:         func(string) (os.FileInfo, error) { return reportDirStat{}, nil },
		Now:          func() time.Time { return reportNow },
		Pid:          func() int { return 99 },
		Getwd:        func() (string, error) { return "", errors.New("unused") },
		Abs:          func(s string) (string, error) { return s, nil },
		EvalSymlinks: func(s string) (string, error) { return s, nil },
	}
}

func fakeSpawnDeps() spawntrace.Deps {
	t0 := reportNow
	clk := clock.NewFake(t0)
	ticks := make(chan time.Time, 10)
	for i := 0; i < 10; i++ {
		ticks <- t0
	}
	base := []process.Info{{PID: 1, Command: "launchd", StartTime: t0.Add(-time.Hour)}, {PID: 100, PPID: 1, Command: "zsh", StartTime: t0}}
	child := append(append([]process.Info{}, base...), process.Info{PID: 200, PPID: 100, Command: "make", StartTime: t0})
	tables := [][]process.Info{base, child, base}
	n := 0
	return spawntrace.Deps{
		Collect: func(context.Context) []process.Info {
			clk.Advance(time.Second)
			i := n
			n++
			return tables[i%len(tables)]
		},
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Clock:  clk,
	}
}

var lineageProcs = []process.Info{
	{PID: 1, Command: "launchd", User: "root"},
	{PID: 10, PPID: 1, Command: "zsh", User: "me", RSSKiB: 100},
	{PID: 20, PPID: 10, Command: "make", User: "me", RSSKiB: 200, CPUPct: 5},
}

func reportTestServer(t *testing.T, asked *int) *Server {
	t.Helper()
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetCollectors(Collectors{
		Processes: &fakeProcessCollector{procs: lineageProcs},
		Reports: ReportDeps{
			Load:    fakeLoadReportDeps,
			Orphans: func() orphans.ReportDeps { return fakeOrphansReportDeps(asked) },
			Idle:    fakeIdleReportDeps,
			Find: func(_ context.Context, tg owner.Target) (owner.Result, error) {
				return owner.Result{Target: tg}, nil
			},
			Under: func(context.Context, []string) (owner.UnderResult, error) {
				return owner.UnderResult{Incomplete: true, Hint: "denied"}, nil
			},
			Spawns: fakeSpawnDeps,
		},
	})
	return s
}

func callReportTool(t *testing.T, s *Server, name, args string) ToolResult {
	t.Helper()
	h, ok := s.toolHandlers()[name]
	if !ok {
		t.Fatalf("no handler for %q", name)
	}
	return h(context.Background(), json.RawMessage(args))
}

func TestReportToolSchemas(t *testing.T) {
	want := map[string]struct {
		props    []string
		required []string
	}{
		"load":    {[]string{"roots", "max_load_per_cpu", "min_free_gb", "backoff_free_gb", "top"}, nil},
		"orphans": {[]string{"roots", "min_age", "no_daemon"}, []string{"roots"}},
		"lineage": {[]string{"pid"}, []string{"pid"}},
		"owner":   {[]string{"target", "under"}, nil},
		"spawns":  {[]string{"pid", "duration_seconds", "interval_millis"}, nil},
		"idle":    {[]string{"roots", "pids", "min_age", "max_cpu", "window"}, nil},
	}
	defs := map[string]ToolDefinition{}
	for _, d := range toolDefinitions() {
		defs[d.Name] = d
	}
	handlers := (&Server{}).toolHandlers()
	for name, w := range want {
		d, ok := defs[name]
		if !ok {
			t.Fatalf("tool %q not listed", name)
		}
		if _, ok := handlers[name]; !ok {
			t.Fatalf("tool %q has no handler", name)
		}
		schema := d.InputSchema.(map[string]interface{})
		props := schema["properties"].(map[string]interface{})
		if len(props) != len(w.props) {
			t.Errorf("%s properties = %v", name, props)
		}
		for _, p := range w.props {
			if _, ok := props[p]; !ok {
				t.Errorf("%s schema lacks %q", name, p)
			}
		}
		req, _ := schema["required"].([]string)
		if strings.Join(req, ",") != strings.Join(w.required, ",") {
			t.Errorf("%s required = %v, want %v", name, req, w.required)
		}
	}
}

func TestReportToolArgumentValidation(t *testing.T) {
	s := reportTestServer(t, new(int))
	cases := []struct{ tool, args, want string }{
		{"load", `{"top":0}`, "invalid load options"},
		{"load", `{"top":51}`, "top must be 1..50"},
		{"load", `{"min_free_gb":-1}`, "invalid load options"},
		{"load", `{"max_load_per_cpu":0}`, "invalid load options"},
		{"load", `{"roots":["rel"]}`, "absolute"},
		{"load", `{"roots":["/r0","/r1","/r2","/r3","/r4","/r5","/r6","/r7","/r8","/r9","/r10","/r11","/r12","/r13","/r14","/r15","/r16","/r17","/r18","/r19","/r20","/r21","/r22","/r23","/r24","/r25","/r26","/r27","/r28","/r29","/r30","/r31","/r32"]}`, "at most 32"},
		{"load", `{"top":"x"}`, "invalid params"},
		{"orphans", `{}`, "roots is required"},
		{"orphans", `{"roots":["rel"]}`, "absolute"},
		{"orphans", `{"roots":["/w"],"min_age":"soon"}`, "min_age"},
		{"orphans", `{"roots":["/w"],"min_age":"-1s"}`, "negative"},
		{"lineage", `{}`, "positive"},
		{"lineage", `{"pid":-3}`, "positive"},
		{"lineage", `{"pid":4242}`, "no such process"},
		{"owner", `{}`, "exactly one"},
		{"owner", `{"target":":80","under":["/w"]}`, "exactly one"},
		{"owner", `{"target":"./x"}`, "absolute"},
		{"owner", `{"target":"garbage"}`, "unrecognised"},
		{"owner", `{"under":["rel"]}`, "absolute"},
		{"spawns", `{"duration_seconds":31}`, "1..30"},
		{"spawns", `{"duration_seconds":0}`, "1..30"},
		{"spawns", `{"interval_millis":10}`, "at least 50"},
		{"spawns", `{"duration_seconds":1,"interval_millis":2000}`, "must not exceed"},
		{"spawns", `{"pid":-1}`, "must not be negative"},
		{"idle", `{}`, "roots or pids"},
		{"idle", `{"roots":["rel"]}`, "absolute"},
		{"idle", `{"pids":[1],"window":"31s"}`, "at most 30s"},
		{"idle", `{"pids":[1],"window":"10ms"}`, "invalid idle options"},
		{"idle", `{"pids":[1],"max_cpu":0}`, "invalid idle options"},
		{"idle", `{"pids":[0]}`, "invalid pid"},
		{"idle", `{"pids":[1],"min_age":"x"}`, "min_age"},
	}
	for _, tc := range cases {
		res := callReportTool(t, s, tc.tool, tc.args)
		if !res.IsError || !strings.Contains(res.Content[0].Text, tc.want) {
			t.Errorf("%s %s: want error containing %q, got %+v", tc.tool, tc.args, tc.want, res)
		}
	}
}

// requireSameJSON checks a tool's text is the builder's value marshalled
// the way the CLI's --json encoder does it.
func requireSameJSON(t *testing.T, res ToolResult, want any) {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].Text)
	}
	b, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if res.Content[0].Text != string(b) {
		t.Fatalf("tool JSON differs from the builder's:\n--- got\n%s\n--- want\n%s", res.Content[0].Text, b)
	}
}

func TestReportToolsMatchBuilders(t *testing.T) {
	var asked int
	s := reportTestServer(t, &asked)
	ctx := context.Background()

	loadOpts := hostload.DefaultReportOptions()
	loadOpts.Roots, loadOpts.Top = []string{"/r"}, 2
	wantLoad, err := hostload.BuildReport(ctx, loadOpts, fakeLoadReportDeps())
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, callReportTool(t, s, "load", `{"roots":["/r"],"top":2}`), wantLoad)
	if wantLoad.Cause.Kind != hostload.CauseRoots {
		t.Fatalf("fake load cause = %+v", wantLoad.Cause)
	}

	wantOrphans, err := orphans.BuildReport(ctx, orphans.ReportOptions{Roots: []string{"/work"}, MinAge: time.Minute}, fakeOrphansReportDeps(new(int)))
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, callReportTool(t, s, "orphans", `{"roots":["/work"]}`), wantOrphans)
	if asked != 1 || wantOrphans.Total != 2 {
		t.Fatalf("asked daemon %d times, total %d", asked, wantOrphans.Total)
	}
	callReportTool(t, s, "orphans", `{"roots":["/work"],"no_daemon":true}`)
	if asked != 1 {
		t.Fatal("no_daemon still asked the daemon")
	}

	wantLineage, err := lineage.Build(lineageProcs, 10)
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, callReportTool(t, s, "lineage", `{"pid":10}`), wantLineage)

	tg, _ := owner.ParseTarget(":8080", "")
	requireSameJSON(t, callReportTool(t, s, "owner", `{"target":":8080"}`), owner.Result{Target: tg, Holders: []owner.Holder{}})
	requireSameJSON(t, callReportTool(t, s, "owner", `{"under":["/w/../x"]}`),
		owner.UnderResult{Dirs: []string{"/x"}, Holders: []owner.Holder{}, Incomplete: true, Hint: "denied"})

	idleOpts := idle.DefaultReportOptions()
	idleOpts.Roots, idleOpts.Window = []string{"/work/lane"}, 2*time.Second
	wantIdle, err := idle.BuildReport(ctx, idleOpts, fakeIdleReportDeps())
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, callReportTool(t, s, "idle", `{"roots":["/work/lane"],"window":"2s"}`), wantIdle)
	if len(wantIdle.Result.Holders) != 1 {
		t.Fatalf("idle holders = %+v", wantIdle.Result)
	}
}

func TestSpawnsToolReturnsEventsAndSummary(t *testing.T) {
	s := reportTestServer(t, new(int))
	res := callReportTool(t, s, "spawns", `{"duration_seconds":3,"interval_millis":1000}`)
	if res.IsError {
		t.Fatalf("spawns: %s", res.Content[0].Text)
	}
	var got struct {
		Events  []map[string]any `json:"events"`
		Summary map[string]any   `json:"summary"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 2 || got.Events[0]["event"] != "start" || got.Events[1]["event"] != "exit" || got.Events[1]["lifetime_ms"] == nil {
		t.Fatalf("events = %v", got.Events)
	}
	if got.Summary["type"] != "summary" || got.Summary["starts"].(float64) != 1 || got.Summary["duration_seconds"] == nil {
		t.Fatalf("summary = %v", got.Summary)
	}
	want, err := spawntrace.Trace(context.Background(), spawntrace.Options{Interval: time.Second, Duration: 3 * time.Second}, fakeSpawnDeps(), spawnsMaxEvents)
	if err != nil {
		t.Fatal(err)
	}
	requireSameJSON(t, res, want)

	if res := callReportTool(t, s, "spawns", `{"pid":4242,"duration_seconds":3,"interval_millis":1000}`); !res.IsError || !strings.Contains(res.Content[0].Text, "4242") {
		t.Fatalf("unknown pid: %+v", res)
	}
}

func TestOwnerToolTrimsPathTarget(t *testing.T) {
	s := reportTestServer(t, new(int))
	res := callReportTool(t, s, "owner", `{"target":" /tmp"}`)
	if res.IsError && len(res.Content) > 0 && strings.Contains(res.Content[0].Text, "must be absolute") {
		t.Fatalf("a padded absolute path was rejected: %+v", res)
	}
}
