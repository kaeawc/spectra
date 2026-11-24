package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/idle"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// jobCall calls a job tool and returns its result and text.
func jobCall(t *testing.T, cs *sdk.ClientSession, tool, args string) (*sdk.CallToolResult, string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: json.RawMessage(args)})
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	return res, res.Content[0].(*sdk.TextContent).Text
}

// jobRec calls a job tool that must answer with a job record.
func jobRec(t *testing.T, cs *sdk.ClientSession, tool, args string) jobRecord {
	t.Helper()
	res, text := jobCall(t, cs, tool, args)
	if res.IsError {
		t.Fatalf("%s %s: %s", tool, args, text)
	}
	var r jobRecord
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		t.Fatalf("%s record %q: %v", tool, text, err)
	}
	return r
}

func idArg(id string) string { return fmt.Sprintf(`{"job_id":%q}`, id) }

// slowIdleServer's idle sampler takes the window as the real one does, so
// the tool reports elapsed progress while it runs.
func slowIdleServer(t *testing.T) *Server {
	t.Helper()
	srv := reportTestServer(t, new(int))
	srv.collect.Reports.Idle = func() idle.ReportDeps {
		d := fakeIdleReportDeps()
		sample := d.Sample
		d.Sample = func(ctx context.Context, w time.Duration) (idle.Sample, error) {
			select {
			case <-time.After(w):
			case <-ctx.Done():
			}
			return sample(ctx, w)
		}
		return d
	}
	return srv
}

func listedTools(t *testing.T, cs *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	list, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*sdk.Tool{}
	for _, tool := range list.Tools {
		out[tool.Name] = tool
	}
	return out
}

func validateAgainst(t *testing.T, tool *sdk.Tool, res *sdk.CallToolResult) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("%s: no structuredContent", tool.Name)
	}
	if err := resolveOutputSchema(t, tool.OutputSchema).Validate(res.StructuredContent); err != nil {
		t.Fatalf("%s: structuredContent does not validate: %v", tool.Name, err)
	}
	if tool.Name == "job_start" || tool.Name == "job_cancel" {
		if err := resolveOutputSchema(t, jobRecordSchema()).Validate(res.StructuredContent); err != nil {
			t.Fatalf("%s: structuredContent is not a job record: %v", tool.Name, err)
		}
	}
}

func TestJobStartStatusResult(t *testing.T) {
	srv := slowIdleServer(t)
	cs := startServerSession(t, srv, "2025-11-25", nil)
	tools := listedTools(t, cs)
	args := `{"pids":[20],"min_age":"1m","window":"1500ms"}`

	res, _ := jobCall(t, cs, "job_start", `{"tool":"idle","arguments":`+args+`}`)
	validateAgainst(t, tools["job_start"], res)
	rec := jobRec(t, cs, "job_status", `{"job_id":"`+mustJobID(t, res)+`"}`)
	if rec.Tool != "idle" || rec.Status != jobWorking || len(rec.JobID) != 32 || rec.TTL != defaultJobTTL.Milliseconds() || rec.PollInterval != 1000 {
		t.Fatalf("started record = %+v", rec)
	}
	if early, text := jobCall(t, cs, "job_result", idArg(rec.JobID)); !early.IsError || !strings.Contains(text, "still working") {
		t.Fatalf("job_result while working = %s", text)
	}
	waitFor(t, "job progress", func() bool {
		r := jobRec(t, cs, "job_status", idArg(rec.JobID))
		return r.Status == jobWorking && r.Progress != nil && r.Progress.Total == 1.5
	})
	waitFor(t, "job completion", func() bool { return jobRec(t, cs, "job_status", idArg(rec.JobID)).Status == jobCompleted })
	statusRes, _ := jobCall(t, cs, "job_status", idArg(rec.JobID))
	validateAgainst(t, tools["job_status"], statusRes)
	final := jobRec(t, cs, "job_status", idArg(rec.JobID))
	if !final.ResultAvailable || final.ExpiresAt == nil || !final.ExpiresAt.Equal(final.LastUpdatedAt.Add(defaultJobTTL)) {
		t.Fatalf("completed record = %+v", final)
	}

	got, text := jobCall(t, cs, "job_result", idArg(rec.JobID))
	if got.IsError || got.StructuredContent == nil {
		t.Fatalf("job_result = %s", text)
	}
	validateAgainst(t, tools["idle"], got)
	direct, directText := jobCall(t, cs, "idle", args)
	if direct.IsError || !reflect.DeepEqual(direct.StructuredContent, got.StructuredContent) || directText != text {
		t.Fatalf("job result differs from a direct call:\njob    %s\ndirect %s", text, directText)
	}
	listRes, _ := jobCall(t, cs, "job_list", `{}`)
	validateAgainst(t, tools["job_list"], listRes)
}

func mustJobID(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	var r jobRecord
	if err := json.Unmarshal([]byte(res.Content[0].(*sdk.TextContent).Text), &r); err != nil || r.JobID == "" {
		t.Fatalf("no job id in %+v: %v", res, err)
	}
	return r.JobID
}

func TestJobCancelStopsTheTool(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	cs := startServerSession(t, lifecycleServer(spawns, &countingProcessCollector{}), "2025-11-25", nil)
	rec := jobRec(t, cs, "job_start", `{"tool":"spawns","arguments":{"duration_seconds":30}}`)
	waitClosed(t, spawns.started, "spawns to start")
	cancelled := jobRec(t, cs, "job_cancel", idArg(rec.JobID))
	if cancelled.Status != jobCancelled || cancelled.ResultAvailable {
		t.Fatalf("cancel record = %+v", cancelled)
	}
	waitFor(t, "the job's context to end", spawns.cancelled.Load)
	if r := jobRec(t, cs, "job_status", idArg(rec.JobID)); r.Status != jobCancelled {
		t.Fatalf("status after cancel = %+v", r)
	}
	if res, text := jobCall(t, cs, "job_result", idArg(rec.JobID)); !res.IsError || !strings.Contains(text, "cancelled") {
		t.Fatalf("job_result of a cancelled job = %s", text)
	}
	if res, text := jobCall(t, cs, "job_cancel", idArg(rec.JobID)); !res.IsError || !strings.Contains(text, "already cancelled") {
		t.Fatalf("second cancel = %s", text)
	}
}

func TestJobStartValidatesArguments(t *testing.T) {
	srv := reportTestServer(t, new(int))
	cs := startServerSession(t, srv, "2025-11-25", nil)
	for _, tc := range []struct{ args, want string }{
		{`{"tool":"lineage","arguments":{"pid":1}}`, "tool must be one of"},
		{`{}`, "tool must be one of"},
		{`{"tool":"spawns","arguments":[1]}`, "arguments must be an object"},
		{`{"tool":"spawns","arguments":{"duration_seconds":0}}`, "duration_seconds must be 1..30"},
		{`{"tool":"idle","arguments":{}}`, "pass roots or pids"},
		{`{"tool":"orphans","arguments":{"roots":["rel"]}}`, "must be absolute"},
		{`{"tool":"process","arguments":{"operation":"list"}}`, "only for operation sample"},
		{`{"tool":"jvm","arguments":{"operation":"flamegraph","pid":1}}`, "confirm_sensitive"},
		{`{"tool":"jvm","arguments":{"operation":"flamegraph","pid":1,"confirm_sensitive":true,"duration_seconds":600}}`, "duration_seconds must be 1..60"},
		{`{"tool":"jvm","arguments":{"operation":"flamegraph","pid":1,"confirm_sensitive":true,"event":"x;rm"}}`, "event must be one of"},
		{`{"tool":"process","arguments":{"operation":"sample","pid":1,"duration_seconds":3600}}`, "duration_seconds must be 1..60"},
		{`{"tool":"snapshot","arguments":{"operation":"diff"}}`, "only for operation create or baseline"},
		{`{"tool":"triage","arguments":{"pid":"x"}}`, "invalid params"},
	} {
		res, text := jobCall(t, cs, "job_start", tc.args)
		if !res.IsError || !strings.Contains(text, tc.want) {
			t.Errorf("job_start %s = %q, want an error containing %q", tc.args, text, tc.want)
		}
	}
	_, text := jobCall(t, cs, "job_list", `{}`)
	if text != "{\n  \"jobs\": []\n}" {
		t.Fatalf("rejected starts left jobs: %s", text)
	}
	if res, text := jobCall(t, cs, "job_status", `{}`); !res.IsError || !strings.Contains(text, "job_id is required") {
		t.Fatalf("job_status without id = %s", text)
	}
}

// TestJobValidationMatchesHandlers keeps job_start's argument checks in step
// with each wrapped tool's own: bad arguments fail both with the same text,
// and the arguments a direct call accepts pass.
func TestJobValidationMatchesHandlers(t *testing.T) {
	srv := structuredTestServer(t)
	handlers := srv.toolHandlers()
	bad := map[string][]string{
		"spawns":   {`{"duration_seconds":0}`, `{"duration_seconds":31}`, `{"interval_millis":1}`, `{"duration_seconds":"x"}`, `{"duration_seconds":1,"interval_millis":5000}`},
		"idle":     {`{}`, `{"roots":["rel"]}`, `{"pids":[1],"window":"1h"}`, `{"pids":[1],"min_age":"x"}`},
		"load":     {`{"top":500}`, `{"roots":["rel"]}`, `{"min_free_gb":-1}`, `{"top":0}`},
		"orphans":  {`{}`, `{"roots":["rel"]}`, `{"roots":["/a"],"min_age":"-1m"}`, `{"roots":["/a"],"min_age":"bogus"}`},
		"diagnose": {`{"snapshot_id":"x","thread_dump_pids":[1]}`, `{"persist":"x"}`},
		"process":  {`{"operation":"sample"}`, `{"operation":"sample","pid":20,"duration_seconds":61}`, `{"operation":"sample","pid":20,"duration_seconds":-1}`},
		"jvm": {`{"operation":"flamegraph","pid":1}`, `{"operation":"flamegraph","confirm_sensitive":true}`,
			`{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"duration_seconds":61}`,
			`{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"event":"bogus"}`,
			`{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"dest":"/tmp/graph.png"}`},
		"triage":   {`{"pid":"x"}`},
		"snapshot": {`{"operation":5}`},
	}
	for tool, cases := range bad {
		for _, args := range cases {
			_, err := jobTools[tool].validate(json.RawMessage(args))
			direct := handlers[tool](context.Background(), json.RawMessage(args))
			if err == nil || !direct.IsError || err.Error() != direct.Content[0].Text {
				t.Errorf("%s %s: job validation %v, handler %q", tool, args, err, direct.Content[0].Text)
			}
		}
	}
	// good exercises each argument a wrapped tool takes: job validation and
	// a direct call both accept them.
	good := map[string][]string{
		"spawns":   {`{"duration_seconds":1,"interval_millis":1000}`, `{"duration_seconds":3,"interval_millis":1000}`, `{"pid":100,"duration_seconds":2,"interval_millis":1000}`},
		"idle":     {`{"pids":[20]}`, `{"roots":["/work"]}`, `{"pids":[20],"min_age":"1m"}`, `{"pids":[20],"window":"100ms"}`, `{"pids":[20],"max_cpu":5}`},
		"load":     {`{}`, `{"roots":["/r"]}`, `{"max_load_per_cpu":2}`, `{"min_free_gb":1}`, `{"backoff_free_gb":2}`, `{"top":5}`},
		"orphans":  {`{"roots":["/work"]}`, `{"roots":["/work"],"min_age":"1h"}`},
		"triage":   {`{}`, `{"pid":20}`},
		"diagnose": {`{}`, `{"include_raw":true}`, `{"thread_dump_pids":[20]}`, `{"persist":true}`},
		"snapshot": {`{}`, `{"operation":"create"}`, `{"operation":"baseline"}`, `{"operation":"create","store":true,"skip_apps":true,"limit_summary":3}`},
		"process":  {`{"operation":"sample","pid":20}`, `{"operation":"sample","pid":20,"duration_seconds":1}`, `{"operation":"sample","pid":20,"duration_seconds":60}`, `{"operation":"sample","pid":20,"interval_millis":5}`},
		"jvm": {`{"operation":"flamegraph","pid":20,"confirm_sensitive":true}`, `{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"duration_seconds":60}`,
			`{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"event":"wall"}`, `{"operation":"flamegraph","pid":20,"confirm_sensitive":true,"event":"alloc","duration_seconds":1}`},
	}
	srv.SetArtifactDir(filepath.Join(t.TempDir(), "artifacts"))
	srv.collect.JVMs = artifactJVMs{}
	srv.collect.Processes = &sampleProcs{out: "ok"}
	for _, tool := range jobToolNames() {
		cases, ok := good[tool]
		if !ok || len(cases) == 0 {
			t.Errorf("job tool %s has no accept cases", tool)
		}
		if _, ok := handlers[tool]; !ok {
			t.Errorf("job tool %s has no handler", tool)
			continue
		}
		for _, args := range cases {
			if _, err := jobTools[tool].validate(json.RawMessage(args)); err != nil {
				t.Errorf("%s %s: valid arguments rejected: %v", tool, args, err)
			}
			if res := handlers[tool](context.Background(), json.RawMessage(args)); res.IsError {
				t.Errorf("%s %s: direct call failed: %s", tool, args, res.Content[0].Text)
			}
		}
	}
}

// blockingHandler runs until release closes or its context ends.
type blockingHandler struct {
	running, maxRunning, calls atomic.Int32
	release                    chan struct{}
	ctxDone                    atomic.Int32
}

func (b *blockingHandler) handle(ctx context.Context, _ json.RawMessage) ToolResult {
	b.calls.Add(1)
	n := b.running.Add(1)
	for {
		m := b.maxRunning.Load()
		if n <= m || b.maxRunning.CompareAndSwap(m, n) {
			break
		}
	}
	defer b.running.Add(-1)
	select {
	case <-b.release:
	case <-ctx.Done():
		b.ctxDone.Add(1)
	}
	return toolText(map[string]string{"ok": "yes"})
}

func storeStatus(t *testing.T, st *jobStore, owner any, id string) jobRecord {
	t.Helper()
	r, err := st.status(owner, id)
	if err != nil {
		t.Fatalf("status %s: %v", id, err)
	}
	return r
}

func TestJobConcurrencyLimitQueuesThenRefuses(t *testing.T) {
	st := newJobStore(jobConfig{MaxConcurrent: 2, MaxLive: 3}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	var ids []string
	for range 3 {
		r, err := st.start("s", "spawns", "", nil, b.handle)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.JobID)
	}
	waitFor(t, "two jobs running", func() bool { return b.running.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	if n := b.calls.Load(); n != 2 {
		t.Fatalf("%d jobs started, want 2 with the third queued", n)
	}
	if r := storeStatus(t, st, "s", ids[2]); r.Status != jobWorking || !strings.HasPrefix(r.StatusMessage, "queued") {
		t.Fatalf("third job = %+v, want working and queued", r)
	}
	if _, err := st.start("s", "spawns", "", nil, b.handle); err == nil || !strings.Contains(err.Error(), "already working") {
		t.Fatalf("fourth live job: %v, want refused", err)
	}
	close(b.release)
	waitFor(t, "every job to complete", func() bool {
		for _, id := range ids {
			if storeStatus(t, st, "s", id).Status != jobCompleted {
				return false
			}
		}
		return true
	})
	if m := b.maxRunning.Load(); m != 2 {
		t.Fatalf("at most %d ran at once, want 2", m)
	}
}

func TestJobCancelWhileQueuedNeverRuns(t *testing.T) {
	st := newJobStore(jobConfig{MaxConcurrent: 1}, nil)
	defer st.close()
	b := &blockingHandler{release: make(chan struct{})}
	first, _ := st.start("s", "spawns", "", nil, b.handle)
	queued, _ := st.start("s", "spawns", "", nil, b.handle)
	waitFor(t, "first job running", func() bool { return b.running.Load() == 1 })
	if _, err := st.cancelJob("s", queued.JobID); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	waitFor(t, "first job done", func() bool { return storeStatus(t, st, "s", first.JobID).Status == jobCompleted })
	if n := b.calls.Load(); n != 1 {
		t.Fatalf("handler ran %d times; the cancelled queued job must not run", n)
	}
}

func TestJobTTLExpiry(t *testing.T) {
	clk := clock.NewFake(reportNow)
	st := newJobStore(jobConfig{Clock: clk, TTL: time.Minute}, nil)
	defer st.close()
	r, err := st.start("s", "load", "", nil, func(context.Context, json.RawMessage) ToolResult { return toolText(map[string]int{"n": 1}) })
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "completion", func() bool { return storeStatus(t, st, "s", r.JobID).Status == jobCompleted })
	clk.Advance(59 * time.Second)
	if _, err := st.result("s", r.JobID); err != nil {
		t.Fatalf("result before ttl: %v", err)
	}
	clk.Advance(time.Second)
	if _, err := st.status("s", r.JobID); !errors.Is(err, errUnknownJob) {
		t.Fatalf("status after ttl: %v, want unknown job", err)
	}
	if l := st.list("s"); len(l.Jobs) != 0 {
		t.Fatalf("expired job still listed: %+v", l)
	}
}

func TestJobFinishedRetentionIsBounded(t *testing.T) {
	st := newJobStore(jobConfig{MaxFinished: 2}, nil)
	defer st.close()
	quick := func(context.Context, json.RawMessage) ToolResult { return toolText(map[string]int{"n": 1}) }
	var ids []string
	for range 3 {
		r, _ := st.start("s", "load", "", nil, quick)
		waitFor(t, "completion", func() bool { s, err := st.status("s", r.JobID); return err == nil && s.Status == jobCompleted })
		ids = append(ids, r.JobID)
	}
	if _, err := st.status("s", ids[0]); !errors.Is(err, errUnknownJob) {
		t.Fatalf("oldest finished job kept past MaxFinished: %v", err)
	}
	if len(st.list("s").Jobs) != 2 {
		t.Fatalf("kept %+v", st.list("s"))
	}
}

func TestJobResultSizeCap(t *testing.T) {
	st := newJobStore(jobConfig{MaxResultBytes: 64}, nil)
	defer st.close()
	big := func(context.Context, json.RawMessage) ToolResult { return toolText(strings.Repeat("x", 100)) }
	r, _ := st.start("s", "spawns", "", nil, big)
	waitFor(t, "failure", func() bool { return storeStatus(t, st, "s", r.JobID).Status == jobFailed })
	rec := storeStatus(t, st, "s", r.JobID)
	if rec.ResultAvailable || !strings.Contains(rec.StatusMessage, "over the 64-byte job limit") {
		t.Fatalf("oversized job = %+v", rec)
	}
	if _, err := st.result("s", r.JobID); err == nil || !strings.Contains(err.Error(), "failed with no result") {
		t.Fatalf("result of an oversized job: %v", err)
	}
}

func TestJobToolErrorIsFailedWithResult(t *testing.T) {
	st := newJobStore(jobConfig{}, nil)
	defer st.close()
	r, _ := st.start("s", "spawns", "", nil, func(context.Context, json.RawMessage) ToolResult { return toolError("boom") })
	waitFor(t, "failure", func() bool { return storeStatus(t, st, "s", r.JobID).Status == jobFailed })
	res, err := st.result("s", r.JobID)
	if err != nil || !res.IsError || res.Content[0].Text != "boom" {
		t.Fatalf("result = %+v, %v; want the tool's error result", res, err)
	}
}

// TestJobsAreScopedToTheirSession connects two clients to one server: the
// second cannot see, read or cancel the first one's job.
func TestJobsAreScopedToTheirSession(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	srv := lifecycleServer(spawns, &countingProcessCollector{})
	sdkSrv := srv.sdkServer()
	t.Cleanup(srv.jobs.close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	connect := func() *sdk.ClientSession {
		st, ct := sdk.NewInMemoryTransports()
		ss, err := sdkSrv.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close(); _ = ss.Wait() })
		return cs
	}
	a, b := connect(), connect()
	rec := jobRec(t, a, "job_start", `{"tool":"spawns","arguments":{"duration_seconds":30}}`)
	for _, tool := range []string{"job_status", "job_result", "job_cancel"} {
		if res, text := jobCall(t, b, tool, idArg(rec.JobID)); !res.IsError || !strings.Contains(text, "unknown job_id") {
			t.Errorf("%s from another session = %s", tool, text)
		}
	}
	if _, text := jobCall(t, b, "job_list", `{}`); strings.Contains(text, rec.JobID) {
		t.Fatalf("another session lists the job: %s", text)
	}
	if _, text := jobCall(t, a, "job_list", `{}`); !strings.Contains(text, rec.JobID) {
		t.Fatalf("the owning session does not list its job: %s", text)
	}
	if r := jobRec(t, a, "job_cancel", idArg(rec.JobID)); r.Status != jobCancelled {
		t.Fatalf("owner cancel = %+v", r)
	}
}

func TestShutdownCancelsJobs(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	srv := lifecycleServer(spawns, &countingProcessCollector{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	srv.reader, srv.writer = serverIn, serverOut
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
		_ = serverOut.Close()
	}()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	rec := jobRec(t, cs, "job_start", `{"tool":"spawns","arguments":{"duration_seconds":30}}`)
	waitClosed(t, spawns.started, "spawns to start")
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if !spawns.cancelled.Load() {
		t.Fatal("shutdown did not cancel the running job")
	}
	srv.jobs.mu.Lock()
	j := srv.jobs.jobs[rec.JobID]
	status := j.status
	srv.jobs.mu.Unlock()
	if status != jobCancelled {
		t.Fatalf("job status after shutdown = %s", status)
	}
	if _, err := srv.jobs.start("s", "spawns", "", nil, (&blockingHandler{}).handle); err == nil {
		t.Fatal("a closed store accepted a job")
	}
}

// TestLongJobDoesNotBlockToolCalls runs a direct call and the job tools
// while a job holds its tool open.
func TestLongJobDoesNotBlockToolCalls(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	cs := startServerSession(t, lifecycleServer(spawns, &countingProcessCollector{}), "2026-07-28", nil)
	rec := jobRec(t, cs, "job_start", `{"tool":"spawns","arguments":{"duration_seconds":30}}`)
	waitClosed(t, spawns.started, "spawns to start")
	done := make(chan *sdk.CallToolResult, 1)
	go func() {
		res, _ := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "lineage", Arguments: map[string]any{"pid": 20}})
		done <- res
	}()
	select {
	case res := <-done:
		if res == nil || res.IsError {
			t.Fatalf("lineage during a job = %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a direct call waited behind a running job")
	}
	if r := jobRec(t, cs, "job_status", idArg(rec.JobID)); r.Status != jobWorking {
		t.Fatalf("status = %+v", r)
	}
	close(spawns.release)
	waitFor(t, "the job to complete", func() bool { return jobRec(t, cs, "job_status", idArg(rec.JobID)).Status == jobCompleted })
	if res, text := jobCall(t, cs, "job_result", idArg(rec.JobID)); res.IsError || !strings.Contains(text, `"summary"`) {
		t.Fatalf("spawns job result = %s", text)
	}
}

func TestJobIDsAreUnguessable(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, err := newJobID()
		if err != nil || len(id) != 32 || seen[id] {
			t.Fatalf("id %q (err %v) repeated or malformed", id, err)
		}
		seen[id] = true
	}
}
