package daemonprofile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/nativeprofile"
	"github.com/kaeawc/spectra/internal/process"
)

const callGraph = `Process:         Finder [412]
Call graph:
    100 Thread_1   DispatchQueue_1: com.apple.main-thread  (serial)
      100 start  (in dyld) + 1  [0x1]
        100 main  (in App) + 2  [0x2]
          100 work  (in App) + 3  [0x3]
`

// serve registers s on a real daemon socket and returns a client.
func serve(t *testing.T, s *Service) *daemonclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spdpf-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	RegisterMethods(server, s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; s.Close(5 * time.Second) })
	for i := 0; i < 200; i++ {
		if c, err := daemonclient.Dial(ctx, paths.Socket); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
	return nil
}

func fixedSample(context.Context, process.SampleOptions) (process.SampleResult, error) {
	return process.SampleResult{PID: 412, TakenAt: time.Unix(1700000000, 0).UTC(), DurationMS: 1000, IntervalMS: 10, Output: callGraph}, nil
}

// gate blocks captures until ctx ends, recording that each started and how
// it ended.
type gate struct {
	started chan struct{}
	ended   chan error
}

func newGate() *gate { return &gate{started: make(chan struct{}, 8), ended: make(chan error, 8)} }

func (g *gate) wait(ctx context.Context) error {
	g.started <- struct{}{}
	<-ctx.Done()
	g.ended <- ctx.Err()
	return ctx.Err()
}

func (g *gate) sample(ctx context.Context, _ process.SampleOptions) (process.SampleResult, error) {
	return process.SampleResult{}, g.wait(ctx)
}

func (g *gate) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("capture never started")
	}
}

func (g *gate) awaitEnd(t *testing.T) error {
	t.Helper()
	select {
	case err := <-g.ended:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not observe ctx ending")
		return nil
	}
}

func call(t *testing.T, s *Service, h func(*Service) daemon.Handler, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return h(s)(context.Background(), &daemon.Request{Params: raw})
}

func start(s *Service) daemon.Handler  { return s.startRPC }
func status(s *Service) daemon.Handler { return s.statusRPC }
func result(s *Service) daemon.Handler { return s.resultRPC }
func cancel(s *Service) daemon.Handler { return s.cancelRPC }

// waitDone polls until the job leaves working.
func waitDone(t *testing.T, s *Service, id string) Job {
	t.Helper()
	for i := 0; i < 500; i++ {
		v, err := call(t, s, status, IDParams{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		if rec := v.(Job); rec.Status != StatusWorking {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestInvalidParamsRejected(t *testing.T) {
	c := serve(t, NewService(Options{OS: hostos.Darwin, SampleCapture: fixedSample}))
	for _, tc := range []struct{ name, method, params string }{
		{"start no kind", MethodStart, `{}`},
		{"start null", MethodStart, `null`},
		{"start bad kind", MethodStart, `{"kind":"hang","pid":1}`},
		{"start wrong type", MethodStart, `{"kind":"sample","pid":"1"}`},
		{"sample no pid", MethodStart, `{"kind":"sample"}`},
		{"sample negative pid", MethodStart, `{"kind":"sample","pid":-1}`},
		{"sample input", MethodStart, `{"kind":"sample","pid":1,"input":"/r.txt"}`},
		{"sample out", MethodStart, `{"kind":"sample","pid":1,"out":"/g.svg"}`},
		{"sample fractional duration", MethodStart, `{"kind":"sample","pid":1,"duration_ms":1500}`},
		{"sample long duration", MethodStart, `{"kind":"sample","pid":1,"duration_ms":61000}`},
		{"sample negative duration", MethodStart, `{"kind":"sample","pid":1,"duration_ms":-1000}`},
		{"sample big interval", MethodStart, `{"kind":"sample","pid":1,"interval_ms":5000}`},
		{"sample negative interval", MethodStart, `{"kind":"sample","pid":1,"interval_ms":-1}`},
		{"spindump nothing", MethodStart, `{"kind":"spindump"}`},
		{"spindump both", MethodStart, `{"kind":"spindump","pid":1,"input":"/r.txt"}`},
		{"spindump relative input", MethodStart, `{"kind":"spindump","input":"r.txt"}`},
		{"spindump input duration", MethodStart, `{"kind":"spindump","input":"/r.txt","duration_ms":1000}`},
		{"spindump out", MethodStart, `{"kind":"spindump","pid":1,"out":"/raw.txt"}`},
		{"spindump interval", MethodStart, `{"kind":"spindump","pid":1,"interval_ms":10}`},
		{"flamegraph no out", MethodStart, `{"kind":"flamegraph","pid":1}`},
		{"flamegraph relative out", MethodStart, `{"kind":"flamegraph","pid":1,"out":"g.svg"}`},
		{"flamegraph relative input", MethodStart, `{"kind":"flamegraph","input":"s.txt","out":"/g.svg"}`},
		{"flamegraph interval", MethodStart, `{"kind":"flamegraph","pid":1,"out":"/g.svg","interval_ms":10}`},
		{"status no id", MethodStatus, `{}`},
		{"status unknown id", MethodStatus, `{"id":"nope"}`},
		{"result unknown id", MethodResult, `{"id":"nope"}`},
		{"cancel unknown id", MethodCancel, `{"id":"nope"}`},
		{"cancel wrong type", MethodCancel, `{"id":7}`},
		{"list wrong type", MethodList, `[1]`},
	} {
		err := c.Call(context.Background(), tc.method, json.RawMessage(tc.params), nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
			t.Errorf("%s: err = %v, want invalid params", tc.name, err)
		}
	}
}

func TestUnsupportedOffDarwinStartsNothing(t *testing.T) {
	s := NewService(Options{OS: hostos.Linux, SampleCapture: func(context.Context, process.SampleOptions) (process.SampleResult, error) {
		t.Error("capture ran off macOS")
		return process.SampleResult{}, nil
	}})
	c := serve(t, s)
	for kind, params := range map[string]StartParams{
		KindSample:     {Kind: KindSample, PID: 1},
		KindSpindump:   {Kind: KindSpindump, Input: "/r.txt"},
		KindFlamegraph: {Kind: KindFlamegraph, PID: 1, Out: "/g.svg"},
	} {
		err := c.Call(context.Background(), MethodStart, params, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
			t.Fatalf("%s: err = %v", kind, err)
		}
		var r hostos.CommandReport
		if err := json.Unmarshal(rpcErr.Data, &r); err != nil {
			t.Fatal(err)
		}
		if r.Command != kind || r.Error != "unsupported_platform" || r.OS != "linux" || r.Feature != features[kind] || r.Hint != nativeprofile.OffDarwinHint {
			t.Fatalf("%s: report = %+v", kind, r)
		}
	}
	var list JobList
	if err := c.Call(context.Background(), MethodList, nil, &list); err != nil || len(list.Jobs) != 0 {
		t.Fatalf("list = %+v, %v; want no jobs", list, err)
	}
}

func TestSampleJobLifecycle(t *testing.T) {
	var got process.SampleOptions
	s := NewService(Options{OS: hostos.Darwin, SampleCapture: func(ctx context.Context, o process.SampleOptions) (process.SampleResult, error) {
		got = o
		return fixedSample(ctx, o)
	}})
	v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 412})
	if err != nil {
		t.Fatal(err)
	}
	rec := v.(Job)
	if rec.Status != StatusWorking || rec.ID == "" || rec.PollIntervalMS != 1000 || rec.DeadlineAt.Sub(rec.CreatedAt) != time.Second+DefaultSlack {
		t.Fatalf("start record = %+v", rec)
	}
	done := waitDone(t, s, rec.ID)
	if done.Status != StatusCompleted || !done.ResultAvailable || done.ExpiresAt == nil {
		t.Fatalf("done record = %+v", done)
	}
	if got.Duration != time.Second || got.IntervalMS != 10 || got.Store {
		t.Fatalf("capture options = %+v, want the CLI defaults without caching", got)
	}
	v, err = call(t, s, result, IDParams{ID: rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := fixedSample(context.Background(), got)
	want, _ := json.Marshal(nativeprofile.BuildSampleReport(res))
	if string(v.(json.RawMessage)) != string(want) {
		t.Fatalf("result = %s\nwant %s", v, want)
	}
}

func TestResultWhileWorkingAndCancel(t *testing.T) {
	g := newGate()
	s := NewService(Options{OS: hostos.Darwin, SampleCapture: g.sample})
	v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := v.(Job).ID
	g.awaitStart(t)
	_, err = call(t, s, result, IDParams{ID: id})
	var je jobError
	if !errors.As(err, &je) || je.rec.Status != StatusWorking || !strings.Contains(err.Error(), "still working") {
		t.Fatalf("result while working: %v", err)
	}
	v, err = call(t, s, cancel, IDParams{ID: id})
	if err != nil || v.(Job).Status != StatusCancelled {
		t.Fatalf("cancel = %+v, %v", v, err)
	}
	if err := g.awaitEnd(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture saw %v, want canceled", err)
	}
	_, err = call(t, s, result, IDParams{ID: id})
	if !errors.As(err, &je) || je.rec.Status != StatusCancelled {
		t.Fatalf("result after cancel: %v", err)
	}
	// Cancelling a finished job is a no-op that returns its record.
	if v, err := call(t, s, cancel, IDParams{ID: id}); err != nil || v.(Job).Status != StatusCancelled {
		t.Fatalf("second cancel = %+v, %v", v, err)
	}
}

func TestJobDeadlineFailsJob(t *testing.T) {
	g := newGate()
	s := NewService(Options{OS: hostos.Darwin, Slack: 20 * time.Millisecond, FlamegraphCapture: func(ctx context.Context, _, _ int) (string, error) {
		return "", g.wait(ctx)
	}})
	v, err := call(t, s, start, StartParams{Kind: KindFlamegraph, PID: 1, DurationMS: 1000, Out: filepath.Join(t.TempDir(), "g.svg")})
	if err != nil {
		t.Fatal(err)
	}
	g.awaitStart(t)
	if err := g.awaitEnd(t); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("capture saw %v", err)
	}
	rec := waitDone(t, s, v.(Job).ID)
	if rec.Status != StatusFailed || !strings.Contains(rec.Error, "timed out after 1.02s") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestFailedJobCarriesCLIError(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin, SampleCapture: func(context.Context, process.SampleOptions) (process.SampleResult, error) {
		return process.SampleResult{}, errors.New("sample: no such process")
	}})
	v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 9})
	if err != nil {
		t.Fatal(err)
	}
	rec := waitDone(t, s, v.(Job).ID)
	if rec.Status != StatusFailed || rec.Error != "sample failed for PID 9: sample: no such process" {
		t.Fatalf("record = %+v", rec)
	}
	_, err = call(t, s, result, IDParams{ID: rec.ID})
	if err == nil || err.Error() != rec.Error {
		t.Fatalf("result err = %v", err)
	}
}

func TestMaxRunningBoundsJobs(t *testing.T) {
	g := newGate()
	s := NewService(Options{OS: hostos.Darwin, MaxRunning: 1, SampleCapture: g.sample})
	v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	g.awaitStart(t)
	if _, err := call(t, s, start, StartParams{Kind: KindSample, PID: 2}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start err = %v", err)
	}
	if _, err := call(t, s, cancel, IDParams{ID: v.(Job).ID}); err != nil {
		t.Fatal(err)
	}
	g.awaitEnd(t)
	// The slot frees once the cancelled capture has returned.
	for i := 0; ; i++ {
		_, err := call(t, s, start, StartParams{Kind: KindSample, PID: 3})
		if err == nil {
			break
		}
		if i > 500 {
			t.Fatalf("slot never freed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFinishedJobsExpireAndAreCapped(t *testing.T) {
	var mu sync.Mutex
	// Anchored to the real time: job deadlines are real context deadlines
	// computed from this clock, so a clock in the past would expire them.
	now := time.Now().UTC()
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	s := NewService(Options{OS: hostos.Darwin, Now: clock, MaxFinished: 2, ResultTTL: time.Minute, SampleCapture: fixedSample})
	var ids []string
	for i := 0; i < 3; i++ {
		v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.(Job).ID)
		if rec := waitDone(t, s, ids[i]); rec.Status != StatusCompleted {
			t.Fatalf("job %s = %s (%s), want completed", ids[i], rec.Status, rec.Error)
		}
		advance(time.Second)
	}
	v, err := s.listRPC(context.Background(), &daemon.Request{})
	if err != nil {
		t.Fatal(err)
	}
	list := v.(JobList)
	if len(list.Jobs) != 2 || list.Jobs[0].ID != ids[2] || list.Jobs[1].ID != ids[1] {
		t.Fatalf("list after cap = %+v, want the two newest", list.Jobs)
	}
	advance(time.Minute)
	if _, err := call(t, s, status, IDParams{ID: ids[2]}); err == nil {
		t.Fatal("job outlived its TTL")
	}
}

func TestPanicAndOversizedResultFailJob(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin, SampleCapture: func(context.Context, process.SampleOptions) (process.SampleResult, error) { panic("boom") }})
	v, _ := call(t, s, start, StartParams{Kind: KindSample, PID: 1})
	if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "panicked: boom") {
		t.Fatalf("panic record = %+v", rec)
	}
	s = NewService(Options{OS: hostos.Darwin, MaxResultBytes: 10, SampleCapture: fixedSample})
	v, _ = call(t, s, start, StartParams{Kind: KindSample, PID: 1})
	if rec := waitDone(t, s, v.(Job).ID); rec.Status != StatusFailed || !strings.Contains(rec.Error, "over the 10-byte limit") {
		t.Fatalf("oversized record = %+v", rec)
	}
}

func TestCloseCancelsJobs(t *testing.T) {
	g := newGate()
	s := NewService(Options{OS: hostos.Darwin, SampleCapture: g.sample})
	v, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	g.awaitStart(t)
	s.Close(5 * time.Second)
	if err := g.awaitEnd(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture saw %v", err)
	}
	s.mu.Lock()
	st := s.jobs[v.(Job).ID].rec.Status
	s.mu.Unlock()
	if st != StatusCancelled {
		t.Fatalf("status after close = %s", st)
	}
	if _, err := call(t, s, start, StartParams{Kind: KindSample, PID: 1}); err == nil {
		t.Fatal("start after close succeeded")
	}
}
