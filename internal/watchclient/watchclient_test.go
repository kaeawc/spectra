package watchclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/metrics"
)

func testServer(t *testing.T) daemon.Paths {
	t.Helper()
	// A short /tmp path stays below the Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	s := daemon.New(daemon.Options{Paths: paths, Version: "dev"})
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	alert := hostwatch.Alert{ID: "a1", Key: "load", Severity: "warning", Title: "High load", State: "firing", FiredAt: now}
	sample := hostwatch.Sample{At: now, NCPU: 8, Load1: 4, Spawn: &hostwatch.SpawnState{ProcsTotal: 7}}
	s.Register("watch.current", func(context.Context, *daemon.Request) (any, error) {
		return map[string]any{"sample": sample, "active_alerts": []hostwatch.Alert{alert}}, nil
	})
	s.Register("watch.samples", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Sample{sample}, nil })
	s.Register("alerts.list", func(context.Context, *daemon.Request) (any, error) { return []hostwatch.Alert{alert}, nil })
	s.Register("alerts.ack", func(context.Context, *daemon.Request) (any, error) { return alert, nil })
	s.Register("process.history", func(context.Context, *daemon.Request) (any, error) {
		return []metrics.Sample{{PID: 42, TakenAt: now}}, nil
	})
	s.Register("alerts.subscribe", func(_ context.Context, req *daemon.Request) (any, error) {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = req.Notify("alerts.event", hostwatch.Event{Type: "fired", Alert: alert})
			_ = req.Notify("watch.sample", sample)
		}()
		return map[string]bool{"subscribed": true}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 100; i++ {
		if client, ok := Connect(context.Background(), nil, paths); ok {
			_ = client.Close()
			return paths
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return paths
}

func TestConnectAndRoundTrips(t *testing.T) {
	paths := testServer(t)
	ctx := context.Background()
	w, ok := Connect(ctx, func(string) string { return "" }, paths)
	if !ok {
		t.Fatal("connect failed")
	}
	defer w.Close()
	if err := w.CheckVersion(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	sample, alerts, err := w.Current(ctx)
	if err != nil || sample.NCPU != 8 || sample.Spawn == nil || sample.Spawn.ProcsTotal != 7 || len(alerts) != 1 {
		t.Fatalf("current: %+v %+v %v", sample, alerts, err)
	}
	rows, err := w.Samples(ctx, time.Time{}, 5)
	if err != nil || len(rows) != 1 {
		t.Fatalf("samples: %+v %v", rows, err)
	}
	listed, err := w.Alerts(ctx, "firing", 5)
	if err != nil || len(listed) != 1 {
		t.Fatalf("alerts: %+v %v", listed, err)
	}
	acked, err := w.Ack(ctx, "a1")
	if err != nil || acked.ID != "a1" {
		t.Fatalf("ack: %+v %v", acked, err)
	}
	history, err := w.ProcessHistory(ctx, 42, 5)
	if err != nil || len(history) != 1 || history[0].PID != 42 {
		t.Fatalf("history: %+v %v", history, err)
	}
	subCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	events, samples := 0, 0
	err = w.Subscribe(subCtx, true, func(hostwatch.Event) {
		events++
		if events > 0 && samples > 0 {
			cancel()
		}
	}, func(hostwatch.Sample) {
		samples++
		if events > 0 && samples > 0 {
			cancel()
		}
	})
	if err != nil || events != 1 || samples != 1 {
		t.Fatalf("subscribe: events=%d samples=%d err=%v", events, samples, err)
	}
}

func TestConnectDisabledAndMissing(t *testing.T) {
	paths := testServer(t)
	for _, value := range []string{"1", "true", "TRUE"} {
		if _, ok := Connect(context.Background(), func(string) string { return value }, paths); ok {
			t.Fatalf("connected with %q", value)
		}
	}
	if _, ok := Connect(context.Background(), nil, daemon.Paths{Socket: filepath.Join(paths.Dir, "missing.sock")}); ok {
		t.Fatal("connected to missing socket")
	}
}
