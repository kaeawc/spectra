package daemoncrash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/crashquery"
	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
)

// serve registers s on a real daemon socket and returns a client.
func serve(t *testing.T, s *Service) *daemonclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spdc-")
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

func TestInvalidParamsRejected(t *testing.T) {
	c := serve(t, NewService(Options{OS: hostos.Darwin, Home: "/Users/me"}))
	for _, tc := range []struct {
		name, method, params string
	}{
		{"list relative dir", MethodList, `{"dir":"Reports"}`},
		{"list negative limit", MethodList, `{"dir":"/r","limit":-1}`},
		{"list wrong type", MethodList, `{"limit":"5"}`},
		{"signatures relative dir", MethodSignatures, `{"dir":"./r"}`},
		{"oom negative limit", MethodOOM, `{"limit":-3}`},
		{"oom not an object", MethodOOM, `[1]`},
		{"inspect no path", MethodInspect, `{}`},
		{"inspect null", MethodInspect, `null`},
		{"inspect relative path", MethodInspect, `{"path":"MyApp.ips"}`},
		{"inspect wrong type", MethodInspect, `{"path":"/r/a.ips","source_lines":"yes"}`},
		{"symbolicate no binary", MethodSymbolicate, `{"load_address":"0x1000","addresses":["0x1010"]}`},
		{"symbolicate relative binary", MethodSymbolicate, `{"binary":"App","load_address":"0x1000","addresses":["0x1010"]}`},
		{"symbolicate flag binary", MethodSymbolicate, `{"binary":"-o","load_address":"0x1000","addresses":["0x1010"]}`},
		{"symbolicate no load address", MethodSymbolicate, `{"binary":"/App","addresses":["0x1010"]}`},
		{"symbolicate bad load address", MethodSymbolicate, `{"binary":"/App","load_address":"zz","addresses":["0x1010"]}`},
		{"symbolicate bad arch", MethodSymbolicate, `{"binary":"/App","load_address":"0x1000","arch":"-printHeader","addresses":["0x1010"]}`},
		{"symbolicate no addresses", MethodSymbolicate, `{"binary":"/App","load_address":"0x1000"}`},
		{"symbolicate flag address", MethodSymbolicate, `{"binary":"/App","load_address":"0x1000","addresses":["-fullPath"]}`},
		{"symbolicate wrong type", MethodSymbolicate, `{"binary":"/App","load_address":"0x1000","addresses":"0x1010"}`},
	} {
		err := c.Call(context.Background(), tc.method, json.RawMessage(tc.params), nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
			t.Errorf("%s: err = %v, want invalid params", tc.name, err)
		}
	}
	tooMany := SymbolicateParams{Binary: "/App", LoadAddress: "0x1000", Addresses: make([]string, MaxAddresses+1)}
	for i := range tooMany.Addresses {
		tooMany.Addresses[i] = fmt.Sprintf("0x%x", 0x1000+i)
	}
	err := c.Call(context.Background(), MethodSymbolicate, tooMany, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
		t.Errorf("too many addresses: err = %v", err)
	}
}

func TestDefaultsMatchCLIFlags(t *testing.T) {
	var gotDir string
	files := make([]string, 30)
	for i := range files {
		files[i] = fmt.Sprintf("/r/%02d.ips", i)
	}
	report := `{"app_name":"A","timestamp":"2026-08-05 10:00:00.00 -0500","bug_type":"309","name":"A"}
{"procName":"A","pid":1,"exception":{"type":"EXC_CRASH"},"faultingThread":0,"threads":[],"usedImages":[]}`
	s := NewService(Options{OS: hostos.Darwin, Home: "/Users/me", Source: func(dir string) crashquery.Source {
		gotDir = dir
		return crashquery.Source{
			List: func(context.Context) ([]string, error) { return files, nil },
			Read: func(string) ([]byte, error) { return []byte(report), nil },
		}
	}})
	v, err := s.listRPC(context.Background(), &daemon.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != "/Users/me/Library/Logs/DiagnosticReports" {
		t.Errorf("default dir = %q", gotDir)
	}
	if n := len(v.([]crashquery.Summary)); n != crashquery.DefaultLimit {
		t.Errorf("default limit kept %d rows, want %d", n, crashquery.DefaultLimit)
	}
	v, err = s.listRPC(context.Background(), &daemon.Request{Params: json.RawMessage(`{"limit":0}`)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(v.([]crashquery.Summary)); n != len(files) {
		t.Errorf("limit 0 kept %d rows, want all %d", n, len(files))
	}
}

func TestNoHomeOnDarwin(t *testing.T) {
	s := NewService(Options{OS: hostos.Darwin})
	s.opts.Home = ""
	_, err := s.oomRPC(context.Background(), &daemon.Request{})
	if err == nil || !strings.Contains(err.Error(), "cannot resolve the home directory") {
		t.Fatalf("err = %v", err)
	}
}

// blocker records that work started and returns once ctx ends.
type blocker struct {
	started chan struct{}
	stopped chan error
}

func newBlocker() *blocker {
	return &blocker{started: make(chan struct{}, 8), stopped: make(chan error, 8)}
}

// wait never blocks on the channels, since source-line resolution may call
// it once per image after ctx ended.
func (b *blocker) wait(ctx context.Context) error {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case b.stopped <- ctx.Err():
	default:
	}
	return ctx.Err()
}

// blockingRunner blocks every subprocess until ctx ends, as a killed child
// would.
type blockingRunner struct{ b *blocker }

func (r blockingRunner) Run(ctx context.Context, _ proc.Cmd) (proc.Result, error) {
	return proc.Result{}, r.b.wait(ctx)
}

// sourceLinesFixture is a report with one app image whose binary exists, so
// source_lines runs atos.
func sourceLinesFixture(t *testing.T) (string, crashreport.SourceFS) {
	t.Helper()
	data, err := os.ReadFile("../crashreport/testdata/frames.ips")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "frames.ips")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, crashreport.OSSourceFS()
}

func blockingService(b *blocker, timeout time.Duration, fs crashreport.SourceFS) *Service {
	return NewService(Options{
		OS: hostos.Darwin,
		Source: func(string) crashquery.Source {
			return crashquery.Source{
				List: func(ctx context.Context) ([]string, error) { return nil, b.wait(ctx) },
				Read: os.ReadFile,
			}
		},
		SourceLines:  crashreport.SourceLineOptions{Runner: blockingRunner{b}, FS: fs, Home: "/nonexistent"},
		Symbolicate:  func(ctx context.Context, _ string, _ ...string) ([]byte, error) { return nil, b.wait(ctx) },
		SweepTimeout: timeout, InspectTimeout: timeout, SymbolicateTimeout: timeout,
	})
}

type blockingCall struct {
	method string
	params string
	rpc    func(*Service) daemon.Handler
}

func blockingCalls(inspectPath string) []blockingCall {
	return []blockingCall{
		{MethodList, `{"dir":"/r"}`, func(s *Service) daemon.Handler { return s.listRPC }},
		{MethodSignatures, `{"dir":"/r"}`, func(s *Service) daemon.Handler { return s.signaturesRPC }},
		{MethodOOM, `{"dir":"/r"}`, func(s *Service) daemon.Handler { return s.oomRPC }},
		{MethodInspect, `{"path":"` + inspectPath + `","source_lines":true}`, func(s *Service) daemon.Handler { return s.inspectRPC }},
		{MethodSymbolicate, `{"binary":"/App","load_address":"0x1000","addresses":["0x1010"]}`, func(s *Service) daemon.Handler { return s.symbolicateRPC }},
	}
}

// Closing the client connection cancels the work: the fake sees ctx end and
// the call returns without a (partial) value.
func TestClientCancellationStopsWork(t *testing.T) {
	path, fs := sourceLinesFixture(t)
	for _, bc := range blockingCalls(path) {
		b := newBlocker()
		rpc := bc.rpc(blockingService(b, time.Minute, fs))
		done := make(chan struct{})
		type out struct {
			v   any
			err error
		}
		res := make(chan out, 1)
		go func() {
			v, err := rpc(context.Background(), &daemon.Request{Params: json.RawMessage(bc.params), Done: done})
			res <- out{v, err}
		}()
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work never started", bc.method)
		}
		close(done)
		select {
		case r := <-res:
			if r.v != nil || !errors.Is(r.err, context.Canceled) {
				t.Fatalf("%s: got %v, %v; want canceled", bc.method, r.v, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: call did not return after cancel", bc.method)
		}
		select {
		case err := <-b.stopped:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: work saw %v", bc.method, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe cancellation", bc.method)
		}
	}
}

func TestServerDeadlineStopsWork(t *testing.T) {
	path, fs := sourceLinesFixture(t)
	for _, bc := range blockingCalls(path) {
		b := newBlocker()
		rpc := bc.rpc(blockingService(b, 20*time.Millisecond, fs))
		_, err := rpc(context.Background(), &daemon.Request{Params: json.RawMessage(bc.params)})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 20ms") {
			t.Fatalf("%s: err = %v", bc.method, err)
		}
		select {
		case <-b.stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe the deadline", bc.method)
		}
	}
}

func TestPanicBecomesError(t *testing.T) {
	s := NewService(Options{Source: func(string) crashquery.Source {
		return crashquery.Source{List: func(context.Context) ([]string, error) { panic("boom") }}
	}})
	_, err := s.listRPC(context.Background(), &daemon.Request{Params: json.RawMessage(`{"dir":"/r"}`)})
	if err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestSymbolicateUnsupportedOffDarwinCarriesReport(t *testing.T) {
	c := serve(t, NewService(Options{OS: hostos.Linux}))
	err := c.Call(context.Background(), MethodSymbolicate, SymbolicateParams{Binary: "/App", LoadAddress: "0x1000", Addresses: []string{"0x1010"}}, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
		t.Fatalf("err = %v", err)
	}
	var r hostos.CommandReport
	if err := json.Unmarshal(rpcErr.Data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Command != "symbolicate" || r.Error != "unsupported_platform" || r.Feature != "atos symbolication" || r.OS != "linux" {
		t.Fatalf("report = %+v", r)
	}
}
