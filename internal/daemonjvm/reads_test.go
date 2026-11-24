package daemonjvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/jvm"
)

// serveReads registers the read methods of s on a real daemon socket.
func serveReads(t *testing.T, s *Service) *daemonclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spdjr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "d.sock"), Lock: filepath.Join(dir, "d.lock"), PID: filepath.Join(dir, "d.pid")}
	server := daemon.New(daemon.Options{Paths: paths, Version: "test"})
	RegisterReadMethods(server, s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
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

var readMethods = []string{MethodExplain, MethodGCStats, MethodHeapHistogram, MethodThreadDump, MethodVMMemory, MethodNMT}

func TestReadInvalidParamsRejected(t *testing.T) {
	var ran atomic.Bool
	c := serveReads(t, NewService(Options{Runner: func(context.Context) jvm.CmdRunner {
		return func(string, ...string) ([]byte, error) { ran.Store(true); return nil, errors.New("unexpected") }
	}}))
	type tc struct{ name, method, params string }
	var cases []tc
	for _, m := range readMethods {
		cases = append(cases,
			tc{"absent pid", m, ``},
			tc{"null", m, `null`},
			tc{"zero pid", m, `{"pid":0}`},
			tc{"negative pid", m, `{"pid":-4}`},
			tc{"string pid", m, `{"pid":"12"}`},
			tc{"not an object", m, `[12]`},
		)
	}
	cases = append(cases,
		tc{"negative suspects", MethodHeapHistogram, `{"pid":12,"suspects":-1}`},
		tc{"unknown nmt mode", MethodNMT, `{"pid":12,"mode":"baseline"}`},
		tc{"negative samples", MethodExplain, `{"pid":12,"samples":-1}`},
		tc{"too many samples", MethodExplain, `{"pid":12,"samples":121}`},
		tc{"negative interval", MethodExplain, `{"pid":12,"interval_ms":-1}`},
		tc{"trend window too long", MethodExplain, `{"pid":12,"samples":3,"interval_ms":600000}`},
		tc{"huge interval", MethodExplain, `{"pid":12,"interval_ms":9223372036854775807}`},
		tc{"soft_refs not bool", MethodExplain, `{"pid":12,"soft_refs":"yes"}`},
	)
	for _, tc := range cases {
		var params any
		if tc.params != "" {
			params = json.RawMessage(tc.params)
		}
		err := c.Call(context.Background(), tc.method, params, nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
			t.Errorf("%s %s: err = %v, want invalid params", tc.method, tc.name, err)
		}
	}
	if ran.Load() {
		t.Fatal("a JDK tool ran for invalid params")
	}
}

func TestReadMethodsRegistered(t *testing.T) {
	c := serveReads(t, NewService(Options{}))
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range readMethods {
		if !slices.Contains(st.Methods, m) {
			t.Errorf("daemon.status methods %v missing %s", st.Methods, m)
		}
	}
}

func TestExplainDefaultsMatchCLI(t *testing.T) {
	opts, err := ExplainParams{PID: 12}.explainOptions()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Samples != 1 || opts.Interval != time.Second || !opts.SoftRefs {
		t.Fatalf("defaults = %+v", opts)
	}
	off := false
	opts, err = ExplainParams{PID: 12, Samples: 3, IntervalMS: 250, SoftRefs: &off}.explainOptions()
	if err != nil || opts.Samples != 3 || opts.Interval != 250*time.Millisecond || opts.SoftRefs {
		t.Fatalf("explicit = %+v, %v", opts, err)
	}
}

// readBlocker is a JDK runner that blocks until its call's ctx ends.
type readBlocker struct {
	started chan struct{}
	stopped chan error
}

func newReadBlocker() *readBlocker {
	return &readBlocker{started: make(chan struct{}, 8), stopped: make(chan error, 8)}
}

func (b *readBlocker) service(timeout time.Duration) *Service {
	return NewService(Options{ReadTimeout: timeout, Runner: func(ctx context.Context) jvm.CmdRunner {
		return func(string, ...string) ([]byte, error) {
			b.started <- struct{}{}
			<-ctx.Done()
			b.stopped <- ctx.Err()
			return nil, ctx.Err()
		}
	}})
}

func readRPC(s *Service, method string) func(context.Context, *daemon.Request) (any, error) {
	return map[string]func(context.Context, *daemon.Request) (any, error){
		MethodExplain:       s.explainRPC,
		MethodGCStats:       s.gcStatsRPC,
		MethodHeapHistogram: s.heapHistogramRPC,
		MethodThreadDump:    s.threadDumpRPC,
		MethodVMMemory:      s.vmMemoryRPC,
		MethodNMT:           s.nmtRPC,
	}[method]
}

func pidReq(done <-chan struct{}) *daemon.Request {
	return &daemon.Request{Params: json.RawMessage(`{"pid":12}`), Done: done}
}

// Closing the client connection kills the JDK tool: the fake runner sees its
// ctx end and the call returns without a (partial) value.
func TestReadClientCancellationStopsWork(t *testing.T) {
	for _, method := range readMethods {
		b := newReadBlocker()
		rpc := readRPC(b.service(time.Minute), method)
		done := make(chan struct{})
		type out struct {
			v   any
			err error
		}
		res := make(chan out, 1)
		go func() {
			v, err := rpc(context.Background(), pidReq(done))
			res <- out{v, err}
		}()
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work never started", method)
		}
		close(done)
		select {
		case r := <-res:
			if r.v != nil || !errors.Is(r.err, context.Canceled) || !strings.HasPrefix(r.err.Error(), method+": ") {
				t.Fatalf("%s: got %v, %v; want canceled", method, r.v, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: call did not return after cancel", method)
		}
		select {
		case err := <-b.stopped:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: work saw %v", method, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe cancellation", method)
		}
	}
}

func TestReadServerDeadlineStopsWork(t *testing.T) {
	for _, method := range readMethods {
		b := newReadBlocker()
		_, err := readRPC(b.service(20*time.Millisecond), method)(context.Background(), pidReq(nil))
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), method+": timed out after ") {
			t.Fatalf("%s: err = %v", method, err)
		}
		select {
		case <-b.stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe the deadline", method)
		}
	}
}

// jvm.explain's deadline grows with its trend window.
func TestExplainTimeoutCoversTrendWindow(t *testing.T) {
	b := newReadBlocker()
	s := b.service(20 * time.Millisecond)
	req := &daemon.Request{Params: json.RawMessage(`{"pid":12,"samples":3,"interval_ms":10}`)}
	_, err := s.explainRPC(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "timed out after 40ms") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadPanicBecomesError(t *testing.T) {
	s := NewService(Options{Runner: func(context.Context) jvm.CmdRunner {
		return func(string, ...string) ([]byte, error) { panic("boom") }
	}})
	if _, err := s.gcStatsRPC(context.Background(), pidReq(nil)); err == nil || err.Error() != "jvm.gc_stats: panicked: boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestThreadDumpCarriesCodeSources(t *testing.T) {
	const dump = "\"main\" #1 prio=5 os_prio=31 tid=0x1 nid=0x103 runnable\n   java.lang.Thread.State: RUNNABLE\n\tat com.example.App.serve(App.java:42)\n"
	var gotPID int
	s := NewService(Options{
		Runner: func(context.Context) jvm.CmdRunner {
			return func(string, ...string) ([]byte, error) { return []byte(dump), nil }
		},
		CodeSources: func(_ context.Context, pid int, d jvm.ParsedThreadDump) map[string]jvm.CodeSource {
			gotPID = pid
			return map[string]jvm.CodeSource{"com.example.App": {Jar: "/srv/app.jar", JarEntry: "com/example/App.class"}}
		},
	})
	v, err := s.threadDumpRPC(context.Background(), pidReq(nil))
	if err != nil {
		t.Fatal(err)
	}
	rep := v.(jvm.ThreadDumpReport)
	if gotPID != 12 || rep.CodeSources["com.example.App"].Jar != "/srv/app.jar" {
		t.Fatalf("pid=%d code_sources=%v, want the dump's classes resolved as the CLI does", gotPID, rep.CodeSources)
	}
}
