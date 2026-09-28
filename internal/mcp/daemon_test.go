package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/watchclient"
)

func mcpTestDaemon(t *testing.T) daemon.Paths {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	s := daemon.New(daemon.Options{Paths: p})
	a := hostwatch.Alert{ID: "a1", Title: "High load", State: "firing"}
	s.Register("watch.current", func(context.Context, *daemon.Request) (any, error) {
		return map[string]any{"sample": hostwatch.Sample{NCPU: 8, Load1: 4}, "active_alerts": []hostwatch.Alert{a}}, nil
	})
	s.Register("watch.samples", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Sample{{NCPU: 8}}, nil })
	s.Register("alerts.list", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Alert{a}, nil })
	s.Register("alerts.ack", func(context.Context, *daemon.Request) (any, error) { return a, nil })
	s.Register("process.history", func(context.Context, *daemon.Request) (any, error) { return []metrics.Sample{{PID: 42}}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 100; i++ {
		if client, ok := watchclient.Connect(context.Background(), nil, p); ok {
			_ = client.Close()
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not start")
	return p
}

func TestDaemonTools(t *testing.T) {
	p := mcpTestDaemon(t)
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetDaemonConnector(func(ctx context.Context) (daemonWatchClient, bool) { return watchclient.Connect(ctx, nil, p) })
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{"host_health", `{"samples":1}`, `"recent_samples"`},
		{"alerts", `{"action":"list"}`, "High load"},
		{"alerts", `{"action":"ack","id":"a1"}`, "a1"},
		{"process", `{"operation":"history","pid":42}`, `"pid"`},
	} {
		result := s.toolHandlers()[tc.name](json.RawMessage(tc.args))
		if result.IsError || !strings.Contains(result.Content[0].Text, tc.want) {
			t.Errorf("%s: %+v", tc.name, result)
		}
	}
}

func TestDaemonToolsUnavailable(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.SetDaemonConnector(func(context.Context) (daemonWatchClient, bool) { return nil, false })
	for _, name := range []string{"host_health", "alerts"} {
		result := s.toolHandlers()[name](json.RawMessage(`{}`))
		if !result.IsError || !strings.Contains(result.Content[0].Text, `"error":"daemon_unavailable"`) || !strings.Contains(result.Content[0].Text, "spectra daemon start") {
			t.Errorf("%s: %+v", name, result)
		}
	}
	result := s.toolProcess(json.RawMessage(`{"operation":"history","pid":42}`))
	if !result.IsError || !strings.Contains(result.Content[0].Text, "spectra daemon start") {
		t.Errorf("history: %+v", result)
	}
}

func TestFiringAlertsResource(t *testing.T) {
	p := mcpTestDaemon(t)
	var out bytes.Buffer
	s := NewServer(strings.NewReader(""), &out)
	s.SetDaemonConnector(func(ctx context.Context) (daemonWatchClient, bool) { return watchclient.Connect(ctx, nil, p) })
	s.handleResourcesRead(Request{ID: 1, Params: json.RawMessage(`{"uri":"spectra://alerts/firing"}`)})
	if !strings.Contains(out.String(), "High load") || !strings.Contains(out.String(), "application/json") {
		t.Fatalf("resource: %s", out.String())
	}
}

func startMCPTestDaemon(t *testing.T, p daemon.Paths, register func(*daemon.Server)) (func(), <-chan error) {
	t.Helper()
	d := daemon.New(daemon.Options{Paths: p})
	register(d)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	for i := 0; i < 100; i++ {
		if client, ok := watchclient.Connect(context.Background(), nil, p); ok {
			_ = client.Close()
			return func() { cancel(); <-done }, done
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("daemon did not start")
	return nil, done
}

func reconnectPaths(t *testing.T) daemon.Paths {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
}

func registerCurrent(d *daemon.Server, load float64, handler func(context.Context, *daemon.Request) (any, error)) {
	d.Register("watch.current", func(ctx context.Context, req *daemon.Request) (any, error) {
		if handler != nil {
			return handler(ctx, req)
		}
		return map[string]any{"sample": hostwatch.Sample{NCPU: 2, Load1: load}, "active_alerts": []hostwatch.Alert{}}, nil
	})
}

func TestDaemonReconnectsAfterRestart(t *testing.T) {
	p := reconnectPaths(t)
	start := func() func() {
		stop, _ := startMCPTestDaemon(t, p, func(d *daemon.Server) { registerCurrent(d, 1, nil) })
		return stop
	}
	stop := start()
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	var connects atomic.Int32
	s.SetDaemonConnector(func(ctx context.Context) (daemonWatchClient, bool) {
		connects.Add(1)
		return watchclient.Connect(ctx, nil, p)
	})
	call := func() ToolResult { return s.toolHostHealth(json.RawMessage(`{"samples":0}`)) }
	if result := call(); result.IsError {
		t.Fatalf("initial call: %+v", result)
	}
	stop()
	stop = start()
	defer stop()
	result := call()
	if result.IsError || !strings.Contains(result.Content[0].Text, "host load 1.00") {
		t.Fatalf("call after restart: %+v", result)
	}
	if got := connects.Load(); got != 2 {
		t.Fatalf("connector calls = %d, want 2", got)
	}
}

func TestDaemonTimeoutDropsConnectionAndReconnectsNextCall(t *testing.T) {
	p := reconnectPaths(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	var requests atomic.Int32
	stop, _ := startMCPTestDaemon(t, p, func(d *daemon.Server) {
		registerCurrent(d, 0, func(context.Context, *daemon.Request) (any, error) {
			if requests.Add(1) == 1 {
				<-release
			}
			return map[string]any{"sample": hostwatch.Sample{NCPU: 2, Load1: 3}, "active_alerts": []hostwatch.Alert{}}, nil
		})
	})
	defer func() { releaseOnce.Do(func() { close(release) }); stop() }()
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	s.setDaemonCallTimeout(50 * time.Millisecond)
	t.Cleanup(func() { s.setDaemonCallTimeout(0) })
	var connects atomic.Int32
	s.SetDaemonConnector(func(ctx context.Context) (daemonWatchClient, bool) {
		connects.Add(1)
		return watchclient.Connect(ctx, nil, p)
	})
	call := func() ToolResult { return s.toolHostHealth(json.RawMessage(`{"samples":0}`)) }
	result := call()
	if !result.IsError || !strings.Contains(result.Content[0].Text, `"error":"daemon_timeout"`) || !strings.Contains(result.Content[0].Text, "spectra daemon logs") {
		t.Fatalf("timed out call: %+v", result)
	}
	releaseOnce.Do(func() { close(release) })
	result = call()
	if result.IsError || !strings.Contains(result.Content[0].Text, "host load 3.00") {
		t.Fatalf("call after timeout: %+v", result)
	}
	if got := connects.Load(); got != 2 {
		t.Fatalf("connector calls = %d, want 2", got)
	}
}
