package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/metrics"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var liveVersions = []string{"2025-11-25", "2026-07-28"}

// fakeStream is a daemon whose alert stream the test drives.
type fakeStream struct {
	events     chan hostwatch.Event
	samples    chan hostwatch.Sample
	subscribed chan struct{}
	streams    atomic.Int32
	current    hostwatch.Sample // Current's answer; zero means a calm default
	subErr     error            // when set, Subscribe fails before any data
}

func newFakeStream() *fakeStream {
	return &fakeStream{events: make(chan hostwatch.Event), samples: make(chan hostwatch.Sample), subscribed: make(chan struct{}, 8)}
}

func (f *fakeStream) connect(context.Context) (daemonWatchClient, bool) {
	return &fakeStreamClient{f}, true
}

type fakeStreamClient struct{ f *fakeStream }

func (c *fakeStreamClient) Current(context.Context) (hostwatch.Sample, []hostwatch.Alert, error) {
	if c.f.current.NCPU != 0 {
		return c.f.current, []hostwatch.Alert{{ID: "a1"}}, nil
	}
	return hostwatch.Sample{NCPU: 4, Load1: 1, MemoryPressure: "normal"}, []hostwatch.Alert{{ID: "a1"}}, nil
}
func (c *fakeStreamClient) Samples(context.Context, time.Time, int) ([]hostwatch.Sample, error) {
	return nil, nil
}
func (c *fakeStreamClient) Alerts(context.Context, string, int) ([]hostwatch.Alert, error) {
	return []hostwatch.Alert{{ID: "a1", Title: "High load", State: "firing"}}, nil
}
func (c *fakeStreamClient) Ack(context.Context, string) (hostwatch.Alert, error) {
	return hostwatch.Alert{}, nil
}
func (c *fakeStreamClient) ProcessHistory(context.Context, int, int) ([]metrics.Sample, error) {
	return nil, nil
}
func (c *fakeStreamClient) Close() error { return nil }

func (c *fakeStreamClient) Subscribe(ctx context.Context, _ bool, onEvent func(hostwatch.Event), onSample func(hostwatch.Sample)) error {
	c.f.streams.Add(1)
	defer c.f.streams.Add(-1)
	c.f.subscribed <- struct{}{}
	if c.f.subErr != nil {
		return c.f.subErr
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-c.f.events:
			onEvent(e)
		case s := <-c.f.samples:
			onSample(s)
		}
	}
}

type liveSession struct {
	srv     *Server
	cs      *sdk.ClientSession
	updates chan string
	stop    func()
}

// startLiveSession runs srv over pipes and connects a client at version that
// records every resources/updated URI it receives.
func startLiveSession(t *testing.T, srv *Server, version string) *liveSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	srv.reader, srv.writer = serverIn, serverOut
	done := make(chan error, 1)
	go func() {
		done <- srv.Run(ctx)
		_ = serverOut.Close()
	}()
	updates := make(chan string, 64)
	client := sdk.NewClient(&sdk.Implementation{Name: "spectra-test", Version: "0"}, &sdk.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) {
			updates <- req.Params.URI
		},
	})
	cs, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientIn, Writer: clientOut}, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		cancel()
		t.Fatalf("connect at %s: %v", version, err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = cs.Close()
			_ = clientOut.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("server did not stop after the client closed")
			}
			cancel()
		})
	}
	t.Cleanup(stop)
	return &liveSession{srv: srv, cs: cs, updates: updates, stop: stop}
}

func (l *liveSession) expectUpdate(t *testing.T, uri string) {
	t.Helper()
	select {
	case got := <-l.updates:
		if got != uri {
			t.Fatalf("resources/updated for %q, want %q", got, uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no resources/updated for %q", uri)
	}
}

func (l *liveSession) expectNoUpdate(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case got := <-l.updates:
		t.Fatalf("unexpected resources/updated for %q", got)
	case <-time.After(within):
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *liveHub) sessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.sessions)
}

func TestLiveAlertsSubscription(t *testing.T) {
	for _, version := range liveVersions {
		t.Run(version, func(t *testing.T) {
			f := newFakeStream()
			srv := NewServer(strings.NewReader(""), io.Discard)
			srv.SetDaemonConnector(f.connect)
			l := startLiveSession(t, srv, version)
			ctx := context.Background()
			if err := l.cs.Subscribe(ctx, &sdk.SubscribeParams{URI: firingAlertsURI}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			select {
			case <-f.subscribed:
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not follow the daemon stream")
			}
			f.events <- hostwatch.Event{Type: "fired", Alert: hostwatch.Alert{ID: "a2"}}
			l.expectUpdate(t, firingAlertsURI)

			if err := l.cs.Unsubscribe(ctx, &sdk.UnsubscribeParams{URI: firingAlertsURI}); err != nil {
				t.Fatalf("unsubscribe: %v", err)
			}
			waitFor(t, "watcher to stop", func() bool { return !srv.live.running() && f.streams.Load() == 0 })
			l.expectNoUpdate(t, 100*time.Millisecond)
		})
	}
}

func TestLiveHostHealthFromDaemonSamples(t *testing.T) {
	for _, version := range liveVersions {
		t.Run(version, func(t *testing.T) {
			f := newFakeStream()
			srv := NewServer(strings.NewReader(""), io.Discard)
			srv.SetDaemonConnector(f.connect)
			srv.liveCfg = liveConfig{healthMinInterval: time.Millisecond}
			l := startLiveSession(t, srv, version)
			if err := l.cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: hostHealthURI}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			<-f.subscribed
			f.samples <- hostwatch.Sample{NCPU: 4, Load1: 1, MemoryPressure: "normal"}
			f.samples <- hostwatch.Sample{NCPU: 4, Load1: 2, MemoryPressure: "normal"} // same level
			l.expectNoUpdate(t, 50*time.Millisecond)
			f.samples <- hostwatch.Sample{NCPU: 4, Load1: 7, MemoryPressure: "normal"} // hold
			l.expectUpdate(t, hostHealthURI)
			f.events <- hostwatch.Event{Type: "fired"} // alerts not subscribed
			l.expectNoUpdate(t, 50*time.Millisecond)

			res, err := l.cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: hostHealthURI})
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var doc hostHealthDoc
			if err := json.Unmarshal([]byte(res.Contents[0].Text), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Daemon != "running" || doc.ActiveAlerts == nil || *doc.ActiveAlerts != 1 || doc.LoadVerdict != "hold" { // the hub's level, as notified
				t.Fatalf("host health = %+v", doc)
			}
		})
	}
}

func TestHealthLevelThresholds(t *testing.T) {
	now := time.Unix(1000, 0)
	var sent []string
	h := newLiveHub(liveConfig{healthMinInterval: 10 * time.Second}, nil, func() time.Time { return now })
	h.notify = func(uri string) { sent = append(sent, uri) }
	h.counts[hostHealthURI] = 1
	h.gen = 1
	step := func(d time.Duration, s hostwatch.Sample, want int) {
		t.Helper()
		now = now.Add(d)
		h.observe(1, s, true)
		if len(sent) != want {
			t.Fatalf("after %+v: %d notifications, want %d", s, len(sent), want)
		}
	}
	base := hostwatch.Sample{NCPU: 4, Load1: 2, MemoryPressure: "normal"}
	step(0, base, 0) // baseline
	moved := base
	moved.Load1, moved.MemFreePct, moved.SwapUsedMB = 5, 3, 900 // raw values only
	step(time.Second, moved, 0)
	hold := base
	hold.Load1 = 6 // 1.5 per CPU
	step(time.Second, hold, 1)
	hysteresis := base
	hysteresis.Load1 = 5.6 // 1.4 per CPU: still hold
	step(20*time.Second, hysteresis, 1)
	step(20*time.Second, base, 2) // back to admit
	warn := base
	warn.MemoryPressure = "warn"
	step(time.Second, warn, 2)    // inside the minimum interval: held back
	step(time.Second, warn, 2)    // still held back
	step(10*time.Second, warn, 3) // sent once the interval has passed
	hot := warn
	hot.ThermalThrottled = true
	step(10*time.Second, hot, 4)
	h.observe(1, hot, false) // the daemon went away: source is part of the level
	if len(sent) != 4 {
		t.Fatalf("source change inside interval sent early: %v", sent)
	}
	now = now.Add(11 * time.Second)
	h.observe(1, hot, false)
	if len(sent) != 5 {
		t.Fatalf("source change: %v", sent)
	}
	h.observe(2, base, true) // a stale watcher generation is ignored
	if len(sent) != 5 {
		t.Fatalf("stale generation notified: %v", sent)
	}
}

func TestLiveNoDaemonPollsOnlyWhileSubscribed(t *testing.T) {
	for _, version := range liveVersions {
		t.Run(version, func(t *testing.T) {
			var polls atomic.Int32
			var load atomic.Int64
			load.Store(1)
			srv := NewServer(strings.NewReader(""), io.Discard)
			srv.SetDaemonConnector(func(context.Context) (daemonWatchClient, bool) { return nil, false })
			srv.liveCfg = liveConfig{
				pollInterval:      10 * time.Millisecond,
				healthMinInterval: time.Millisecond,
				sampleLocal: func(context.Context) (hostwatch.Sample, error) {
					polls.Add(1)
					return hostwatch.Sample{NCPU: 1, Load1: float64(load.Load()), MemoryPressure: "normal"}, nil
				},
			}
			l := startLiveSession(t, srv, version)
			ctx := context.Background()

			res, err := l.cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: firingAlertsURI})
			if err != nil || !strings.Contains(res.Contents[0].Text, `"daemon":"not running"`) {
				t.Fatalf("alerts without daemon: %v %+v", err, res)
			}
			time.Sleep(30 * time.Millisecond)
			if n := polls.Load(); n != 0 {
				t.Fatalf("polled %d times before any subscription", n)
			}

			if err := l.cs.Subscribe(ctx, &sdk.SubscribeParams{URI: firingAlertsURI}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(30 * time.Millisecond)
			if n := polls.Load(); n != 0 {
				t.Fatalf("alerts-only subscription sampled the host %d times", n)
			}

			if err := l.cs.Subscribe(ctx, &sdk.SubscribeParams{URI: hostHealthURI}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "local polling", func() bool { return polls.Load() >= 3 })
			load.Store(5)
			l.expectUpdate(t, hostHealthURI)

			res, err = l.cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: hostHealthURI})
			if err != nil || !strings.Contains(res.Contents[0].Text, `"daemon":"not running"`) || res.TTLMs != hostHealthTTLMs || res.CacheScope != "private" {
				t.Fatalf("host health without daemon: %v %+v", err, res)
			}

			for _, uri := range []string{hostHealthURI, firingAlertsURI} {
				if err := l.cs.Unsubscribe(ctx, &sdk.UnsubscribeParams{URI: uri}); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "watcher to stop", func() bool { return !srv.live.running() })
			before := polls.Load()
			time.Sleep(50 * time.Millisecond)
			if after := polls.Load(); after != before {
				t.Fatalf("still polling after unsubscribe: %d -> %d", before, after)
			}
		})
	}
}

func TestLiveSessionCloseCleansUp(t *testing.T) {
	for _, version := range liveVersions {
		t.Run(version, func(t *testing.T) {
			baseline := runtime.NumGoroutine()
			f := newFakeStream()
			srv := NewServer(strings.NewReader(""), io.Discard)
			srv.SetDaemonConnector(f.connect)
			l := startLiveSession(t, srv, version)
			for _, uri := range []string{firingAlertsURI, hostHealthURI, "spectra://capabilities"} {
				if err := l.cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: uri}); err != nil {
					t.Fatalf("subscribe %s: %v", uri, err)
				}
			}
			if err := l.cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: "spectra://nope"}); err == nil && version != "2026-07-28" {
				t.Fatal("subscribe to an unknown resource succeeded")
			}
			waitFor(t, "daemon stream", func() bool { return f.streams.Load() == 1 })
			hub := srv.live
			l.stop()
			waitFor(t, "session cleanup", func() bool { return hub.sessionCount() == 0 && !hub.running() && f.streams.Load() == 0 })
			waitFor(t, "goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline })
		})
	}
}

// TestLiveWatcherSharedAcrossSessions connects two sessions to one SDK
// server: both get the update from the single daemon stream, and the stream
// stops only when the last subscriber's session closes.
func TestLiveWatcherSharedAcrossSessions(t *testing.T) {
	f := newFakeStream()
	srv := NewServer(strings.NewReader(""), io.Discard)
	srv.SetDaemonConnector(f.connect)
	sdkSrv := srv.sdkServer()
	t.Cleanup(srv.live.close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var clients []*sdk.ClientSession
	updates := make(chan string, 16)
	for i := 0; i < 2; i++ {
		ct, st := sdk.NewInMemoryTransports()
		ss, err := sdkSrv.Connect(ctx, st, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ss.Close() })
		c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, &sdk.ClientOptions{
			ResourceUpdatedHandler: func(_ context.Context, req *sdk.ResourceUpdatedNotificationRequest) { updates <- req.Params.URI },
		})
		cs, err := c.Connect(ctx, ct, &sdk.ClientSessionOptions{ProtocolVersion: liveVersions[i]})
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.Subscribe(ctx, &sdk.SubscribeParams{URI: firingAlertsURI}); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, cs)
	}
	waitFor(t, "both subscriptions", func() bool { return srv.live.sessionCount() == 2 })
	if n := f.streams.Load(); n != 1 {
		t.Fatalf("%d daemon streams, want 1", n)
	}
	f.events <- hostwatch.Event{Type: "fired"}
	for i := 0; i < 2; i++ {
		select {
		case <-updates:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 sessions notified", i)
		}
	}
	_ = clients[0].Close()
	waitFor(t, "first session cleanup", func() bool { return srv.live.sessionCount() == 1 })
	if !srv.live.running() {
		t.Fatal("watcher stopped while a session is still subscribed")
	}
	_ = clients[1].Close()
	waitFor(t, "watcher to stop", func() bool { return srv.live.sessionCount() == 0 && !srv.live.running() && f.streams.Load() == 0 })
}

func TestLiveSubscribeValidation(t *testing.T) {
	h := newLiveHub(liveConfig{}, nil, time.Now)
	if err := h.subscribe(context.Background(), &sdk.SubscribeRequest{Params: &sdk.SubscribeParams{URI: "spectra://nope"}}); err == nil {
		t.Fatal("unknown URI accepted")
	}
	if err := h.subscribe(context.Background(), &sdk.SubscribeRequest{Params: &sdk.SubscribeParams{URI: "spectra://capabilities"}}); err != nil {
		t.Fatalf("static resource: %v", err)
	}
	h.close()
	if h.running() {
		t.Fatal("closed hub running")
	}
}

type fakeTimers struct {
	mu      sync.Mutex
	pending []func()
}

func (f *fakeTimers) after(_ time.Duration, fn func()) func() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, fn)
	return func() bool { return true }
}

func (f *fakeTimers) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

func (f *fakeTimers) fire() {
	f.mu.Lock()
	fns := f.pending
	f.pending = nil
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func TestLiveAlertBurstCoalesces(t *testing.T) {
	f := newFakeStream()
	timers := &fakeTimers{}
	srv := NewServer(strings.NewReader(""), io.Discard)
	srv.SetDaemonConnector(f.connect)
	srv.liveCfg = liveConfig{afterFunc: timers.after}
	l := startLiveSession(t, srv, "2025-11-25")
	if err := l.cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: firingAlertsURI}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-f.subscribed
	for i := 0; i < 5; i++ {
		f.events <- hostwatch.Event{Type: "fired", Alert: hostwatch.Alert{ID: "a"}}
	}
	l.expectNoUpdate(t, 50*time.Millisecond)
	if n := timers.count(); n != 1 {
		t.Fatalf("scheduled %d timers for one burst, want 1", n)
	}
	timers.fire()
	l.expectUpdate(t, firingAlertsURI)
	l.expectNoUpdate(t, 50*time.Millisecond)
}

func TestLiveStreamFailureFallsBackToLocalPolling(t *testing.T) {
	f := newFakeStream()
	f.subErr = errors.New("method not found")
	var samples atomic.Int32
	srv := NewServer(strings.NewReader(""), io.Discard)
	srv.SetDaemonConnector(f.connect)
	srv.liveCfg = liveConfig{pollInterval: time.Hour, sampleLocal: func(context.Context) (hostwatch.Sample, error) {
		samples.Add(1)
		return hostwatch.Sample{NCPU: 4, Load1: 1}, nil
	}}
	l := startLiveSession(t, srv, "2025-11-25")
	ctx := context.Background()
	for _, uri := range []string{hostHealthURI, firingAlertsURI} {
		if err := l.cs.Subscribe(ctx, &sdk.SubscribeParams{URI: uri}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}
	waitFor(t, "local poll after stream failure", func() bool { return samples.Load() > 0 })
	waitFor(t, "alerts reason", func() bool { return srv.live.alertsUnavailable() != "" })
	res, err := l.cs.ReadResource(ctx, &sdk.ReadResourceParams{URI: firingAlertsURI})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := res.Contents[0].Text
	if !strings.Contains(text, "alerts unavailable") || !strings.Contains(text, "method not found") {
		t.Fatalf("alerts content = %s", text)
	}
}

func TestHostHealthContentUsesHubHysteresis(t *testing.T) {
	f := newFakeStream()
	f.current = hostwatch.Sample{NCPU: 4, Load1: 5.5, MemoryPressure: "normal"} // 1.375/CPU
	srv := NewServer(strings.NewReader(""), io.Discard)
	srv.SetDaemonConnector(f.connect)
	srv.liveCfg = liveConfig{healthMinInterval: time.Millisecond}
	l := startLiveSession(t, srv, "2025-11-25")
	if err := l.cs.Subscribe(context.Background(), &sdk.SubscribeParams{URI: hostHealthURI}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	<-f.subscribed
	f.samples <- hostwatch.Sample{NCPU: 4, Load1: 7, MemoryPressure: "normal"} // hold
	f.samples <- f.current                                                     // inside the hysteresis band
	res, err := l.cs.ReadResource(context.Background(), &sdk.ReadResourceParams{URI: hostHealthURI})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc hostHealthDoc
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.LoadVerdict != "hold" {
		t.Fatalf("load_verdict = %q, want hold (hub keeps hold above 1.35/CPU)", doc.LoadVerdict)
	}
}
