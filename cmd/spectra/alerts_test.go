package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/watchclient"
)

func alertTestServer(t *testing.T) daemon.Paths {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	s := daemon.New(daemon.Options{Paths: p, Version: "dev"})
	a := hostwatch.Alert{ID: "a1", Key: "load", Title: "High load", Detail: "Busy", Severity: "warning", State: "firing", FiredAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	sample := hostwatch.Sample{At: a.FiredAt, NCPU: 8, Load1: 5, MemoryPressure: "warn", DataFreeGB: 50, TopCPU: []hostwatch.ProcStat{{PID: 42, Name: "app", CPUPct: 25}}}
	s.Register("watch.current", func(context.Context, *daemon.Request) (any, error) {
		return map[string]any{"sample": sample, "active_alerts": []hostwatch.Alert{a}}, nil
	})
	s.Register("watch.samples", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Sample{sample}, nil })
	s.Register("alerts.list", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Alert{a}, nil })
	s.Register("alerts.ack", func(context.Context, *daemon.Request) (any, error) { return a, nil })
	s.Register("alerts.subscribe", func(_ context.Context, req *daemon.Request) (any, error) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = req.Notify("alerts.event", hostwatch.Event{Type: "fired", Alert: a})
			_ = req.Notify("watch.sample", sample)
		}()
		return map[string]bool{"subscribed": true}, nil
	})
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
	t.Fatal("server did not start")
	return p
}

func TestAlertsModes(t *testing.T) {
	p := alertTestServer(t)
	deps := defaultAlertDeps()
	deps.connect = func(ctx context.Context) (alertClient, bool) { return watchclient.Connect(ctx, nil, p) }
	deps.now = func() time.Time { return time.Date(2026, 9, 27, 12, 1, 0, 0, time.UTC) }
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--state", "all"}, "SEVERITY"},
		{[]string{"--json"}, `"id":"a1"`},
		{[]string{"ack", "a1"}, "High load"},
		{[]string{"ack", "a1", "--json"}, `"id":"a1"`},
		{[]string{"health"}, "Top processes:"},
		{[]string{"health", "--json"}, `"active_alerts"`},
	} {
		var out, stderr bytes.Buffer
		if code := runAlertsWithIO(tc.args, &out, &stderr, deps); code != 0 {
			t.Fatalf("%v exit %d: %s", tc.args, code, stderr.String())
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%v output %q lacks %q", tc.args, out.String(), tc.want)
		}
	}
}

type cancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
	count  int
}

func (w *cancelWriter) Write(p []byte) (int, error) {
	w.count++
	n, err := w.Buffer.Write(p)
	if w.count >= 2 {
		w.cancel()
	}
	return n, err
}

func TestAlertsWatch(t *testing.T) {
	p := alertTestServer(t)
	for _, asJSON := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		deps := defaultAlertDeps()
		deps.connect = func(ctx context.Context) (alertClient, bool) { return watchclient.Connect(ctx, nil, p) }
		deps.context = func() (context.Context, context.CancelFunc) { return ctx, cancel }
		out := &cancelWriter{cancel: cancel}
		args := []string{"watch", "--samples"}
		if asJSON {
			args = append(args, "--json")
		}
		var stderr bytes.Buffer
		if code := runAlertsWithIO(args, out, &stderr, deps); code != 0 {
			t.Fatalf("watch exit %d: %s", code, stderr.String())
		}
		sampleToken := "sample"
		if asJSON {
			sampleToken = `"load1"`
		}
		if !strings.Contains(out.String(), "fired") || !strings.Contains(out.String(), sampleToken) {
			t.Errorf("watch output %q", out.String())
		}
		cancel()
	}
}

func TestAlertsUnavailable(t *testing.T) {
	deps := defaultAlertDeps()
	deps.disabled = func() bool { return false }
	deps.connect = func(context.Context) (alertClient, bool) { return nil, false }
	var out, stderr bytes.Buffer
	if code := runAlertsWithIO(nil, &out, &stderr, deps); code != 1 || strings.TrimSpace(stderr.String()) != daemonNotRunning {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestAlertsDaemonDisabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		disabled bool
	}{
		{name: "flag", args: []string{"--no-daemon"}},
		{name: "environment", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := defaultAlertDeps()
			deps.disabled = func() bool { return tc.disabled }
			deps.connect = func(context.Context) (alertClient, bool) {
				t.Fatal("connect called while daemon access is disabled")
				return nil, false
			}
			var out, stderr bytes.Buffer
			if code := runAlertsWithIO(tc.args, &out, &stderr, deps); code != 1 || strings.TrimSpace(stderr.String()) != daemonAccessDisabled {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
		})
	}
}
