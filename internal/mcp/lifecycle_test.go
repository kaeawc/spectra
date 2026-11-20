package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawntrace"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// wireSession drives a Server over its stdio framing directly, so tests can
// see exactly which replies are (and are not) written.
type wireSession struct {
	t    *testing.T
	in   io.WriteCloser
	msgs chan map[string]any
	done chan error
}

func startWireSession(t *testing.T, srv *Server) *wireSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	srv.reader, srv.writer = serverIn, serverOut
	r := &wireSession{t: t, in: clientOut, msgs: make(chan map[string]any, 64), done: make(chan error, 1)}
	go func() {
		r.done <- srv.Run(ctx)
		_ = serverOut.Close()
	}()
	go func() {
		defer close(r.msgs)
		sc := bufio.NewScanner(clientIn)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("server wrote non-JSON %q: %v", sc.Text(), err)
				continue
			}
			r.msgs <- m
		}
	}()
	t.Cleanup(func() { _ = clientOut.Close(); _ = clientIn.Close() })
	r.send(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
	r.reply("init")
	r.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return r
}

func (r *wireSession) send(line string) {
	r.t.Helper()
	if _, err := io.WriteString(r.in, line+"\n"); err != nil {
		r.t.Fatalf("write %s: %v", line, err)
	}
}

func (r *wireSession) call(id, tool, args string) {
	r.t.Helper()
	r.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, args))
}

func (r *wireSession) cancel(id string) {
	r.t.Helper()
	r.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":%q}}`, id))
}

// reply returns the next reply, failing if it is not for id. Notifications
// are skipped.
func (r *wireSession) reply(id string) map[string]any {
	r.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-r.msgs:
			if !ok {
				r.t.Fatalf("output closed waiting for reply %q", id)
			}
			if _, isNotification := m["method"]; isNotification {
				continue
			}
			if m["id"] != id {
				r.t.Fatalf("got reply %v, want reply %q", m, id)
			}
			return m
		case <-timeout:
			r.t.Fatalf("no reply %q", id)
		}
	}
}

// blockingSpawnDeps traces one poll that blocks until release closes or the
// call's context ends, recording which happened.
type blockingSpawnDeps struct {
	started   chan struct{}
	release   chan struct{}
	cancelled atomic.Bool
	once      sync.Once
}

func newBlockingSpawnDeps() *blockingSpawnDeps {
	return &blockingSpawnDeps{started: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingSpawnDeps) deps() spawntrace.Deps {
	clk := clock.NewFake(reportNow)
	return spawntrace.Deps{
		Collect: func(ctx context.Context) []process.Info {
			b.once.Do(func() { close(b.started) })
			select {
			case <-b.release:
			case <-ctx.Done():
				b.cancelled.Store(true)
			}
			clk.Advance(time.Hour) // end the trace after this poll
			return []process.Info{{PID: 1, Command: "launchd"}}
		},
		Ticker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		Clock:  clk,
	}
}

type countingProcessCollector struct {
	calls atomic.Int32
}

func (c *countingProcessCollector) CollectProcesses(context.Context, process.CollectOptions) []process.Info {
	c.calls.Add(1)
	return lineageProcs
}

func (c *countingProcessCollector) SampleProcess(context.Context, int, int, int) (string, error) {
	return "", nil
}

func lifecycleServer(spawns *blockingSpawnDeps, procs ProcessCollector) *Server {
	s := NewServer(nil, nil)
	s.SetCollectors(Collectors{Processes: procs, Reports: ReportDeps{Spawns: spawns.deps}})
	return s
}

func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestCancelStopsRunningToolAndSendsNoReply(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	r := startWireSession(t, lifecycleServer(spawns, &countingProcessCollector{}))
	r.call("slow", "spawns", `{"duration_seconds":30}`)
	waitClosed(t, spawns.started, "spawns to start")
	r.cancel("slow")
	// The next reply on the wire must be the ping's: none is sent for "slow".
	r.send(`{"jsonrpc":"2.0","id":"ping","method":"ping"}`)
	r.reply("ping")
	if !spawns.cancelled.Load() {
		t.Fatal("the running tool's context was not cancelled")
	}
	r.call("after", "lineage", `{"pid":20}`)
	if res := r.reply("after"); res["error"] != nil {
		t.Fatalf("call after a cancellation failed: %v", res)
	}
}

func TestCancelWhileQueuedNeverRuns(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	procs := &countingProcessCollector{}
	srv := lifecycleServer(spawns, procs)
	queued := make(chan struct{}, 4)
	srv.onQueued = func() { queued <- struct{}{} }
	r := startWireSession(t, srv)

	r.call("running", "spawns", `{"duration_seconds":30}`)
	waitClosed(t, spawns.started, "spawns to start")
	r.call("queued", "lineage", `{"pid":20}`)
	select {
	case <-queued:
	case <-time.After(10 * time.Second):
		t.Fatal("lineage never queued behind spawns")
	}
	r.cancel("queued")
	close(spawns.release)
	r.reply("running")
	r.call("last", "lineage", `{"pid":20}`)
	r.reply("last")
	if n := procs.calls.Load(); n != 1 {
		t.Fatalf("lineage collected processes %d time(s), want 1 (the cancelled call must not run)", n)
	}
}

func TestEOFWaitsForInFlightReplies(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	r := startWireSession(t, lifecycleServer(spawns, &countingProcessCollector{}))
	r.call("piped", "spawns", `{"duration_seconds":30}`)
	waitClosed(t, spawns.started, "spawns to start")
	_ = r.in.Close()
	select {
	case err := <-r.done:
		t.Fatalf("Run returned (%v) with a call in flight", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(spawns.release)
	res := r.reply("piped")
	if res["error"] != nil || spawns.cancelled.Load() {
		t.Fatalf("in-flight call was cut short at EOF: %v", res)
	}
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the last reply")
	}
}

func TestEOFWaitIsBounded(t *testing.T) {
	spawns := newBlockingSpawnDeps()
	srv := lifecycleServer(spawns, &countingProcessCollector{})
	srv.eofWait = 50 * time.Millisecond
	r := startWireSession(t, srv)
	r.call("stuck", "spawns", `{"duration_seconds":30}`)
	waitClosed(t, spawns.started, "spawns to start")
	_ = r.in.Close()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run waited past eofWait")
	}
	close(spawns.release)
}

func progressSession(t *testing.T) (*sdk.ClientSession, *[]*sdk.ProgressNotificationParams, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var got []*sdk.ProgressNotificationParams
	opts := &sdk.ClientOptions{ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
		mu.Lock()
		got = append(got, req.Params)
		mu.Unlock()
	}}
	srv := reportTestServer(t, new(int))
	// The fake idle sampler returns at once; take the window as the real one does.
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
	return startServerSession(t, srv, "2025-11-25", opts), &got, &mu
}

func TestProgressSentOnlyWithToken(t *testing.T) {
	for _, tc := range []struct {
		tool, args string
	}{
		{"spawns", `{"duration_seconds":3,"interval_millis":1000}`},
		{"idle", `{"pids":[20],"min_age":"1m","window":"700ms"}`},
		{"load", `{}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			cs, got, mu := progressSession(t)
			ctx := context.Background()
			if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: tc.tool, Arguments: json.RawMessage(tc.args)}); err != nil {
				t.Fatal(err)
			}
			params := &sdk.CallToolParams{Name: tc.tool, Arguments: json.RawMessage(tc.args)}
			params.SetProgressToken("tok-" + tc.tool)
			res, err := cs.CallTool(ctx, params)
			if err != nil || res.IsError {
				t.Fatalf("call: %v %+v", err, res)
			}
			// A notification sent before the reply can be dispatched after it.
			deadline := time.Now().Add(2 * time.Second)
			for {
				mu.Lock()
				n := len(*got)
				mu.Unlock()
				if n > 0 || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(*got) == 0 {
				t.Fatal("no progress notifications for a call with a progressToken")
			}
			last := 0.0
			for _, p := range *got {
				if p.ProgressToken != "tok-"+tc.tool {
					t.Errorf("progress token %v; the call without a token must get none", p.ProgressToken)
				}
				if p.Progress <= last {
					t.Errorf("progress %v did not increase past %v", p.Progress, last)
				}
				last = p.Progress
			}
		})
	}
}

func TestReplyWriterDropsCancelledRepliesFromBatches(t *testing.T) {
	tr := newReplyTracker()
	tr.request(idKey(json.RawMessage(`1`)))
	tr.request(idKey(json.RawMessage(`2`)))
	tr.cancel(idKey(json.RawMessage(`2`)))
	var out strings.Builder
	w := &replyWriter{w: &out, t: tr}
	frame := `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":2,"result":{}}]` + "\n"
	if n, err := w.Write([]byte(frame)); err != nil || n != len(frame) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if got := out.String(); got != `[{"jsonrpc":"2.0","id":1,"result":{}}]`+"\n" {
		t.Fatalf("wrote %q, want only reply 1", got)
	}
	if tr.open() != 0 || len(tr.pending) != 0 {
		t.Fatalf("calls still open: %v", tr.pending)
	}
}

func TestProgressIsRateLimited(t *testing.T) {
	var sent []float64
	p := &progressReporter{token: "t", now: time.Now, every: time.Millisecond,
		send: func(_ context.Context, n *sdk.ProgressNotificationParams) error {
			sent = append(sent, n.Progress)
			return nil
		}}
	for i := 1; i <= 100; i++ {
		p.report(context.Background(), float64(i), 100, "")
	}
	if len(sent) != 1 {
		t.Fatalf("sent %v in a burst, want only the first", sent)
	}
	var nilReporter *progressReporter
	nilReporter.step(context.Background(), "no token")
	nilReporter.elapsed(context.Background(), time.Second, "no token")()
	if progressFrom(context.Background()) != nil {
		t.Fatal("a context without a reporter returned one")
	}
	if sent[0] != 1 {
		t.Fatalf("first progress = %v, want 1", sent[0])
	}
}
