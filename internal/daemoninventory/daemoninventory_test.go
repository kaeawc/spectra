package daemoninventory

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
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/snapshotquery"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/timeline"
	"github.com/kaeawc/spectra/internal/toolchain"
)

type emptyReader struct{}

func (emptyReader) ListSnapshots(context.Context, string) ([]store.SnapshotRow, error) {
	return nil, nil
}
func (emptyReader) GetSnapshot(context.Context, string) (store.SnapshotRow, error) {
	return store.SnapshotRow{}, store.ErrNotFound
}
func (emptyReader) GetSnapshotJSON(context.Context, string) ([]byte, error) {
	return nil, store.ErrNotFound
}
func (emptyReader) MostRecentSnapshotOlderThan(context.Context, time.Time) (*snapshot.Snapshot, error) {
	return &snapshot.Snapshot{ID: "old"}, nil
}

var _ snapshotquery.Reader = emptyReader{}

func req(params string, done <-chan struct{}) *daemon.Request {
	r := &daemon.Request{Done: done}
	if params != "" {
		r.Params = json.RawMessage(params)
	}
	return r
}

// serve registers s on a real daemon socket and returns a client.
func serve(t *testing.T, s *Service) *daemonclient.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spdi-")
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
	c := serve(t, NewService(Options{Snapshots: emptyReader{}}))
	for _, tc := range []struct {
		name, method, params string
	}{
		{"toolchain not an object", MethodToolchain, `[1]`},
		{"timeline negative", MethodTimeline, `{"since_ms":-1}`},
		{"timeline wrong type", MethodTimeline, `{"since_ms":"1h"}`},
		{"list unknown kind", MethodSnapshotList, `{"kind":"weird"}`},
		{"diff missing", MethodSnapshotDiff, `null`},
		{"diff empty", MethodSnapshotDiff, `{}`},
		{"diff one id", MethodSnapshotDiff, `{"a":"x"}`},
		{"diff since with ids", MethodSnapshotDiff, `{"since_ms":1000,"a":"x","b":"y"}`},
		{"diff since with baseline", MethodSnapshotDiff, `{"since_ms":1000,"baseline":true}`},
		{"diff negative since", MethodSnapshotDiff, `{"since_ms":-5}`},
	} {
		err := c.Call(context.Background(), tc.method, json.RawMessage(tc.params), nil)
		var rpcErr *daemonclient.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.CodeInvalidParams {
			t.Errorf("%s: err = %v, want invalid params", tc.name, err)
		}
	}
}

func TestSnapshotMethodsWithoutStore(t *testing.T) {
	s := NewService(Options{})
	if _, err := s.snapshotListRPC(context.Background(), req("", nil)); !errors.Is(err, ErrNoStore) {
		t.Fatalf("list err = %v", err)
	}
	if _, err := s.snapshotDiffRPC(context.Background(), req(`{"a":"x","b":"y"}`, nil)); !errors.Is(err, ErrNoStore) {
		t.Fatalf("diff err = %v", err)
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

type blockingSource struct{ b *blocker }

func (blockingSource) Name() string { return "block" }
func (s blockingSource) Events(ctx context.Context, _ time.Time) ([]timeline.Event, error) {
	return nil, s.b.wait(ctx)
}

func blockingService(b *blocker, timeout time.Duration) *Service {
	return NewService(Options{
		Toolchain: func(ctx context.Context) toolchain.CollectOptions {
			return toolchain.CollectOptions{CmdRunner: func(string, ...string) ([]byte, error) { return nil, b.wait(ctx) }}
		},
		TimelineSources: func() []timeline.Source { return []timeline.Source{blockingSource{b}} },
		Snapshots:       emptyReader{},
		Live: func(ctx context.Context) *snapshot.Snapshot {
			_ = b.wait(ctx)
			return &snapshot.Snapshot{ID: "partial"}
		},
		ToolchainTimeout: timeout, TimelineTimeout: timeout, SnapshotDiffTimeout: timeout,
	})
}

func blockingCalls(s *Service) map[string]func(context.Context, *daemon.Request) (any, error) {
	return map[string]func(context.Context, *daemon.Request) (any, error){
		MethodToolchain: func(ctx context.Context, r *daemon.Request) (any, error) { return s.toolchainRPC(ctx, r) },
		MethodTimeline:  func(ctx context.Context, r *daemon.Request) (any, error) { return s.timelineRPC(ctx, r) },
		MethodSnapshotDiff: func(ctx context.Context, r *daemon.Request) (any, error) {
			r.Params = json.RawMessage(`{"since_ms":60000}`)
			return s.snapshotDiffRPC(ctx, r)
		},
	}
}

// Closing the client connection cancels the work: the fake sees ctx end and
// the call returns without a (partial) value.
func TestClientCancellationStopsWork(t *testing.T) {
	for name := range blockingCalls(blockingService(newBlocker(), time.Minute)) {
		b := newBlocker()
		rpc := blockingCalls(blockingService(b, time.Minute))[name]
		done := make(chan struct{})
		type out struct {
			v   any
			err error
		}
		res := make(chan out, 1)
		go func() {
			v, err := rpc(context.Background(), req("", done))
			res <- out{v, err}
		}()
		select {
		case <-b.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work never started", name)
		}
		close(done)
		select {
		case r := <-res:
			if r.v != nil || !errors.Is(r.err, context.Canceled) {
				t.Fatalf("%s: got %v, %v; want canceled", name, r.v, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: call did not return after cancel", name)
		}
		select {
		case err := <-b.stopped:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: work saw %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe cancellation", name)
		}
	}
}

func TestServerDeadlineStopsWork(t *testing.T) {
	for name := range blockingCalls(blockingService(newBlocker(), time.Minute)) {
		b := newBlocker()
		rpc := blockingCalls(blockingService(b, 20*time.Millisecond))[name]
		_, err := rpc(context.Background(), req("", nil))
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out after 20ms") {
			t.Fatalf("%s: err = %v", name, err)
		}
		select {
		case <-b.stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: work did not observe the deadline", name)
		}
	}
}

func TestPanicBecomesError(t *testing.T) {
	s := NewService(Options{TimelineSources: func() []timeline.Source { panic("boom") }})
	if _, err := s.timelineRPC(context.Background(), req("", nil)); err == nil || !strings.Contains(err.Error(), "panicked: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestTimelineDefaultsToOneHour(t *testing.T) {
	var got time.Time
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s := NewService(Options{
		TimelineSources: func() []timeline.Source { return []timeline.Source{sinceSource{&got}} },
		Clock:           fixedClock(now),
	})
	if _, err := s.timelineRPC(context.Background(), req("", nil)); err != nil {
		t.Fatal(err)
	}
	if want := now.Add(-time.Hour); !got.Equal(want) {
		t.Fatalf("since = %v, want %v", got, want)
	}
}

type sinceSource struct{ got *time.Time }

func (sinceSource) Name() string { return "since" }
func (s sinceSource) Events(_ context.Context, since time.Time) ([]timeline.Event, error) {
	*s.got = since
	return nil, nil
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }
