package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jsonrpc"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	// A short /tmp path stays within the 104-byte Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
}

func startServer(t *testing.T, opts Options) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(opts).Run(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(opts.Paths.PID); err == nil {
			return cancel, done
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatal("server did not start")
	return nil, nil
}

func TestServerProtocolLifecycle(t *testing.T) {
	p := testPaths(t)
	s := New(Options{Paths: p, Version: "v1", SocketCheckInterval: 20 * time.Millisecond})
	s.Register("test.invalid", func(context.Context, *Request) (any, error) { return nil, InvalidParams("bad value") })
	s.Register("test.panic", func(context.Context, *Request) (any, error) { panic("test panic") })
	s.Register("test.data", func(context.Context, *Request) (any, error) {
		return nil, fmt.Errorf("wrapped: %w", testDataError{})
	})
	s.Register("test.baddata", func(context.Context, *Request) (any, error) { return nil, badDataError{} })
	s.Register("test.oncedata", func(context.Context, *Request) (any, error) { return nil, onceDataError{calls: new(int)} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(p.Socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	request := func(payload string) map[string]json.RawMessage {
		t.Helper()
		// A request the server never answers fails instead of hanging.
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte(payload + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := jsonrpc.ReadMessage(r)
		if err != nil {
			t.Fatal(err)
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal(line, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	status := request(`{"jsonrpc":"2.0","id":1,"method":"daemon.status"}`)
	var st StatusResult
	if err := json.Unmarshal(status["result"], &st); err != nil {
		t.Fatal(err)
	}
	if st.Protocol != ProtocolVersion || st.Version != "v1" || !strings.Contains(strings.Join(st.Methods, ","), "test.invalid") {
		t.Fatalf("status: %+v", st)
	}
	for _, tc := range []struct {
		payload string
		code    int
	}{
		{`{"jsonrpc":"2.0","id":2,"method":"missing"}`, -32601},
		{`{"jsonrpc":"2.0","id":3,"method":"test.invalid"}`, -32602},
		{`{`, -32700},
		{`{"jsonrpc":"2.0","id":4,"method":"test.panic"}`, -32603},
	} {
		resp := request(tc.payload)
		var e struct{ Code int }
		if err := json.Unmarshal(resp["error"], &e); err != nil {
			t.Fatal(err)
		}
		if e.Code != tc.code {
			t.Fatalf("%s: code %d", tc.payload, e.Code)
		}
	}
	dataResp := request(`{"jsonrpc":"2.0","id":6,"method":"test.data"}`)
	if got := string(dataResp["error"]); got != `{"code":-32603,"message":"wrapped: unsupported here","data":{"error":"unsupported_platform"}}` {
		t.Fatalf("data error = %s", got)
	}
	badData := request(`{"jsonrpc":"2.0","id":7,"method":"test.baddata"}`)
	if got := string(badData["error"]); got != `{"code":-32603,"message":"unencodable detail"}` {
		t.Fatalf("unencodable data error = %s", got)
	}
	onceData := request(`{"jsonrpc":"2.0","id":8,"method":"test.oncedata"}`)
	if got := string(onceData["error"]); got != `{"code":-32603,"message":"once detail","data":{"n":1}}` {
		t.Fatalf("once-encodable data error = %s", got)
	}
	_ = request(`{"jsonrpc":"2.0","id":5,"method":"daemon.status"}`)
	_ = request(`{"jsonrpc":"2.0","id":6,"method":"daemon.shutdown"}`)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown hung")
	}
	if _, err := os.Stat(p.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	if _, err := os.Stat(p.PID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid remains: %v", err)
	}
}

func TestSecondRunAndStaleSocket(t *testing.T) {
	p := testPaths(t)
	if err := os.WriteFile(p.Socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancel, done := startServer(t, Options{Paths: p, SocketCheckInterval: 20 * time.Millisecond})
	defer cancel()
	if err := New(Options{Paths: p}).Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second run: %v", err)
	}
	cancel()
	<-done
}

func TestPeerRejectedAndWatchdog(t *testing.T) {
	p := testPaths(t)
	cancel, done := startServer(t, Options{Paths: p, AllowUID: func(uint32) bool { return false }, SocketCheckInterval: 20 * time.Millisecond})
	defer cancel()
	c, err := net.Dial("unix", p.Socket)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, err = jsonrpc.ReadMessage(bufio.NewReader(c))
	_ = c.Close()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("rejected peer read = %v, want EOF", err)
	}
	if err := os.Remove(p.Socket); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not stop")
	}
}

func TestIdleTimeout(t *testing.T) {
	p := testPaths(t)
	_, done := startServer(t, Options{Paths: p, IdleTimeout: 40 * time.Millisecond})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle timeout did not stop server")
	}
}

type flakyListener struct {
	net.Listener
	attempts atomic.Int32
	accepted chan struct{}
}

func (l *flakyListener) Accept() (net.Conn, error) {
	if l.attempts.Add(1) <= 2 {
		return nil, syscall.EMFILE
	}
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted <- struct{}{}
	}
	return c, err
}

func TestAcceptRetriesTemporaryErrors(t *testing.T) {
	path := filepath.Join(testPaths(t).Dir, "retry.sock")
	base, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	ln := &flakyListener{Listener: base, accepted: make(chan struct{}, 1)}
	s := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsOut := make(chan error, 1)
	done := make(chan struct{})
	go func() { s.accept(ctx, ln, errorsOut); close(done) }()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-ln.accepted:
	case <-time.After(time.Second):
		t.Fatal("accept did not retry and accept the connection")
	}
	if ln.attempts.Load() < 3 {
		t.Fatalf("accept attempts = %d", ln.attempts.Load())
	}
	select {
	case err := <-errorsOut:
		t.Fatalf("temporary accept error escaped: %v", err)
	default:
	}
	cancel()
	base.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("accept did not stop after close")
	}
}

func TestProbeHeldLockWithoutPID(t *testing.T) {
	p := testPaths(t)
	lock, err := acquireLock(p.Lock, p.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { unlockFile(lock); lock.Close() }()
	running, pid, err := Probe(p)
	if err != nil || !running || pid != 0 {
		t.Fatalf("Probe = %v, %d, %v", running, pid, err)
	}
}

type testDataError struct{}

func (testDataError) Error() string  { return "unsupported here" }
func (testDataError) ErrorData() any { return map[string]string{"error": "unsupported_platform"} }

type badDataError struct{}

func (badDataError) Error() string  { return "unencodable detail" }
func (badDataError) ErrorData() any { return make(chan int) }

// onceMarshaler encodes once, then fails, as a stateful MarshalJSON might.
type onceMarshaler struct{ calls *int }

func (m onceMarshaler) MarshalJSON() ([]byte, error) {
	*m.calls++
	if *m.calls > 1 {
		return nil, errors.New("second encode")
	}
	return []byte(`{"n":1}`), nil
}

type onceDataError struct{ calls *int }

func (onceDataError) Error() string    { return "once detail" }
func (e onceDataError) ErrorData() any { return onceMarshaler(e) }

// Shutdown hooks run when shutdown starts, before the drain: a handler that
// only returns once the hook has run must not hold Run for the drain timeout.
func TestOnShutdownRunsBeforeDrain(t *testing.T) {
	p := testPaths(t)
	s := New(Options{Paths: p, Version: "v1", SocketCheckInterval: 20 * time.Millisecond})
	hookRan := make(chan struct{})
	s.OnShutdown(func() { close(hookRan) })
	entered := make(chan struct{})
	s.Register("test.block", func(context.Context, *Request) (any, error) {
		close(entered)
		<-hookRan // ignores its own ctx, like a job waiting on its service
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var c net.Conn
	for i := 0; i < 100; i++ {
		var err error
		if c, err = net.Dial("unix", p.Socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("server did not start")
	}
	defer c.Close()
	if _, err := c.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"test.block"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("test.block handler never started")
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if waited := time.Since(start); waited > 3*time.Second {
		t.Fatalf("Run took %s: the hook ran only after the drain timed out", waited)
	}
	select {
	case <-hookRan:
	default:
		t.Fatal("shutdown hook never ran")
	}
}
