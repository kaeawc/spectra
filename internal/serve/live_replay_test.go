package serve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/livehistory"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/telemetry"
)

// seededDaemon starts a daemon whose replay state (ring + collector) is caller
// supplied, so the live.* / process.* handlers can be exercised against known
// data. It returns the backing DB so process.history seeding is possible.
func seededDaemon(t *testing.T, ring *livehistory.Ring, collector *metrics.Collector) (*json.Encoder, *json.Decoder, *store.DB) {
	t.Helper()
	// Use os.MkdirTemp with a short prefix (not t.TempDir): macOS limits Unix
	// socket paths to 104 bytes and t.TempDir embeds the full test name, which
	// exceeds that for these long test names. Mirrors testDaemonWithDB.
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	d := rpc.NewDispatcher()
	registerHandlers(d, "test-version", db, collector, telemetry.NewLiveCollector(), ring,
		cache.Default, nil, nil, &artifact.FakeRecorder{}, artifact.Policy{}, logger.Discard(), nil)

	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go d.ServeListener(ln)
	time.Sleep(10 * time.Millisecond)

	conn, err := rpc.DialUnix(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(10 * time.Second))
	t.Cleanup(func() { conn.Close() })
	return json.NewEncoder(conn), json.NewDecoder(conn), db
}

func TestLiveCurrentEmptyRingErrors(t *testing.T) {
	enc, dec, _ := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())
	resp := rpcCall(t, enc, dec, 80, "live.current", `{}`)
	if resp.Error == nil {
		t.Fatal("expected an error when the ring has no samples")
	}
}

func TestLiveCurrentAndHistoryReplaySeededRing(t *testing.T) {
	ring := livehistory.NewRing(4)
	ring.Add(snapshot.Snapshot{ID: "s1"})
	ring.Add(snapshot.Snapshot{ID: "s2"})
	ring.Add(snapshot.Snapshot{ID: "s3"})
	enc, dec, _ := seededDaemon(t, ring, metrics.NewCollector())

	// live.current returns the most recent snapshot.
	cur := rpcCall(t, enc, dec, 81, "live.current", `{}`)
	if cur.Error != nil {
		t.Fatalf("live.current: %v", cur.Error)
	}
	m, ok := cur.Result.(map[string]any)
	if !ok || m["id"] != "s3" {
		t.Fatalf("live.current id = %v, want s3 (result %T)", m["id"], cur.Result)
	}

	// live.history with a limit returns the last N in chronological order.
	hist := rpcCall(t, enc, dec, 82, "live.history", `{"limit":2}`)
	if hist.Error != nil {
		t.Fatalf("live.history: %v", hist.Error)
	}
	arr, ok := hist.Result.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("live.history len = %v, want 2 (result %T)", len(arr), hist.Result)
	}
	// The last two snapshots (s2, s3) in chronological order — assert both.
	if first := arr[0].(map[string]any); first["id"] != "s2" {
		t.Fatalf("live.history[0] id = %v, want s2", first["id"])
	}
	if last := arr[1].(map[string]any); last["id"] != "s3" {
		t.Fatalf("live.history[1] id = %v, want s3", last["id"])
	}
}

func TestProcessLiveReplaysSeededCollector(t *testing.T) {
	collector := metrics.NewCollector()
	now := time.Now()
	collector.Add(metrics.Sample{PID: 4242, TakenAt: now.Add(-2 * time.Second), RSSKiB: 100})
	collector.Add(metrics.Sample{PID: 4242, TakenAt: now.Add(-1 * time.Second), RSSKiB: 200})
	enc, dec, _ := seededDaemon(t, livehistory.NewRing(4), collector)

	resp := rpcCall(t, enc, dec, 83, "process.live", `{"limit":10}`)
	if resp.Error != nil {
		t.Fatalf("process.live: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	samples, ok := m["4242"].([]any)
	if !ok || len(samples) != 2 {
		t.Fatalf("pid 4242 samples = %v, want 2", m["4242"])
	}
}

func TestProcessHistoryRequiresPID(t *testing.T) {
	enc, dec, _ := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())
	resp := rpcCall(t, enc, dec, 84, "process.history", `{}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("expected CodeInvalidParams for missing pid, got %+v", resp.Error)
	}
}

func TestProcessHistoryReturnsSeededRows(t *testing.T) {
	enc, dec, db := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())

	minute := time.Date(2026, 5, 6, 15, 4, 0, 0, time.UTC)
	rows := []store.ProcessMetricRow{
		{PID: 4242, MinuteAt: minute, AvgRSSKiB: 1000, MaxRSSKiB: 1200, AvgCPUPct: 1.5, MaxCPUPct: 2.0, SampleCount: 30},
		{PID: 4242, MinuteAt: minute.Add(time.Minute), AvgRSSKiB: 2000, MaxRSSKiB: 2400, AvgCPUPct: 2.5, MaxCPUPct: 3.0, SampleCount: 60},
	}
	if err := db.SaveProcessMetrics(context.Background(), rows); err != nil {
		t.Fatalf("seed SaveProcessMetrics: %v", err)
	}

	resp := rpcCall(t, enc, dec, 85, "process.history", `{"pid":4242,"limit":10}`)
	if resp.Error != nil {
		t.Fatalf("process.history: %v", resp.Error)
	}
	arr, ok := resp.Result.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("process.history rows = %v, want 2 (result %T)", len(arr), resp.Result)
	}
	// Newest minute is returned first.
	newest := arr[0].(map[string]any)
	if newest["SampleCount"].(float64) != 60 || newest["MaxRSSKiB"].(float64) != 2400 {
		t.Fatalf("newest row = %+v, want SampleCount 60 / MaxRSSKiB 2400", newest)
	}
}
