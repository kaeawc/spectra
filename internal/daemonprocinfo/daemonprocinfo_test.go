package daemonprocinfo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
)

// serve registers s on a real daemon socket and returns a client.
func serve(t *testing.T, s *Service) *daemonclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spdp-")
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
	c := serve(t, NewService(Options{OS: hostos.Darwin}))
	for _, tc := range []struct {
		name, method, params string
	}{
		{"lineage missing pid", MethodLineage, `null`},
		{"lineage zero pid", MethodLineage, `{"pid":0}`},
		{"lineage wrong type", MethodLineage, `{"pid":"1"}`},
		{"owner no target", MethodOwner, `{}`},
		{"owner bad target", MethodOwner, `{"target":"garbage"}`},
		{"owner bad port", MethodOwner, `{"target":":70000"}`},
		{"owner relative path", MethodOwner, `{"target":"./notes.txt"}`},
		{"owner relative path no dot", MethodOwner, `{"target":"docs/notes.txt"}`},
		{"owner negative timeout", MethodOwner, `{"target":":80","timeout_ms":-1}`},
		{"owner huge timeout", MethodOwner, `{"target":":80","timeout_ms":3600000}`},
		{"owner dirs without under", MethodOwner, `{"target":":80","dirs":["/tmp"]}`},
		{"owner under no dirs", MethodOwner, `{"under":true}`},
		{"owner under relative dir", MethodOwner, `{"under":true,"dirs":["src"]}`},
		{"owner under with target", MethodOwner, `{"under":true,"dirs":["/tmp"],"target":":80"}`},
		{"owner under negative exclude", MethodOwner, `{"under":true,"dirs":["/tmp"],"exclude_pid":-2}`},
		{"services bad domain", MethodServices, `{"domain":"gui"}`},
		{"logs negative last", MethodLogs, `{"last_ms":-1}`},
		{"logs negative top", MethodLogs, `{"top":-1}`},
		{"logs bad grep", MethodLogs, `{"grep":"("}`},
		{"logs wrong type", MethodLogs, `{"process":1}`},
	} {
		err := c.Call(context.Background(), tc.method, json.RawMessage(tc.params), nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
			t.Errorf("%s: err = %v, want invalid params", tc.name, err)
		}
	}
}

func TestUnsupportedOffDarwinCarriesReport(t *testing.T) {
	c := serve(t, NewService(Options{OS: hostos.Linux}))
	err := c.Call(context.Background(), MethodServices, nil, nil)
	var rpcErr *daemonclient.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInternalError {
		t.Fatalf("err = %v", err)
	}
	var r hostos.CommandReport
	if err := json.Unmarshal(rpcErr.Data, &r); err != nil {
		t.Fatal(err)
	}
	if r.Command != "services" || r.Error != "unsupported_platform" || r.Feature != services.Feature || r.Hint != services.OffDarwinHint {
		t.Fatalf("report = %+v", r)
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

func (b *blocker) wait(ctx context.Context) error {
	b.started <- struct{}{}
	<-ctx.Done()
	b.stopped <- ctx.Err()
	return ctx.Err()
}

func blockingService(b *blocker, timeout time.Duration) *Service {
	return NewService(Options{
		OS: hostos.Darwin,
		Processes: process.CollectorFunc(func(ctx context.Context, _ process.CollectOptions) []process.Info {
			_ = b.wait(ctx)
			return []process.Info{{PID: 1, Command: "partial"}}
		}),
		OwnerFind: func(ctx context.Context, _ owner.Target) (owner.Result, error) { return owner.Result{}, b.wait(ctx) },
		OwnerUnder: func(int) owner.UnderFunc {
			return func(ctx context.Context, _ []string) (owner.UnderResult, error) {
				return owner.UnderResult{}, b.wait(ctx)
			}
		},
		Services: services.Options{PlistDirs: []string{"/nonexistent"}, Runner: services.RunnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			return nil, b.wait(ctx)
		})},
		Logs: logquery.Query{OS: hostos.Darwin, Runner: logquery.RunnerFunc(func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
			return nil, nil, b.wait(ctx)
		})},
		LineageTimeout: timeout, ServicesTimeout: timeout, LogsTimeout: timeout,
	})
}

type blockingCall struct {
	method string
	params string
	rpc    func(*Service) daemon.Handler
}

// ownerTimeoutMS is the owner.report timeout_ms the deadline test passes;
// owner takes its cap from params rather than Options.
const ownerTimeoutMS = 20

func blockingCalls() []blockingCall {
	return []blockingCall{
		{MethodLineage, `{"pid":1}`, func(s *Service) daemon.Handler { return s.lineageRPC }},
		{MethodOwner, `{"target":":8080","timeout_ms":20}`, func(s *Service) daemon.Handler { return s.ownerRPC }},
		{MethodOwner + " under", `{"under":true,"dirs":["/tmp"],"timeout_ms":20}`, func(s *Service) daemon.Handler { return s.ownerRPC }},
		{MethodServices, `{}`, func(s *Service) daemon.Handler { return s.servicesRPC }},
		{MethodLogs, `{"process":"x"}`, func(s *Service) daemon.Handler { return s.logsRPC }},
	}
}

// Closing the client connection cancels the work: the fake sees ctx end and
// the call returns without a (partial) value.
func TestClientCancellationStopsWork(t *testing.T) {
	for _, bc := range blockingCalls() {
		b := newBlocker()
		params := strings.Replace(bc.params, `"timeout_ms":20`, `"timeout_ms":60000`, 1)
		rpc := bc.rpc(blockingService(b, time.Minute))
		done := make(chan struct{})
		type out struct {
			v   any
			err error
		}
		res := make(chan out, 1)
		go func() {
			v, err := rpc(context.Background(), &daemon.Request{Params: json.RawMessage(params), Done: done})
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
	for _, bc := range blockingCalls() {
		b := newBlocker()
		rpc := bc.rpc(blockingService(b, ownerTimeoutMS*time.Millisecond))
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
	s := NewService(Options{Processes: process.CollectorFunc(func(context.Context, process.CollectOptions) []process.Info { panic("boom") })})
	_, err := s.lineageRPC(context.Background(), &daemon.Request{Params: json.RawMessage(`{"pid":1}`)})
	if err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestDefaultsMatchCLIFlags(t *testing.T) {
	var got logquery.Query
	s := NewService(Options{OS: hostos.Darwin, Logs: logquery.Query{OS: hostos.Darwin, Runner: logquery.RunnerFunc(func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		got.Predicate = strings.Join(args, " ")
		return nil, nil, nil
	})}})
	if _, err := s.logsRPC(context.Background(), &daemon.Request{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Predicate, "--last 1h") {
		t.Fatalf("log show args %q lack the 1h default window", got.Predicate)
	}
}
