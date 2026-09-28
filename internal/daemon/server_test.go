package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jsonrpc"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
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
