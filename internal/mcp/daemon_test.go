package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
