package serve

import (
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
// supplied, so the live.* / process.* handlers can be exercised against known data.
func seededDaemon(t *testing.T, ring *livehistory.Ring, collector *metrics.Collector) (*json.Encoder, *json.Decoder) {
	t.Helper()
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
		cache.Default, nil, nil, &artifact.FakeRecorder{}, artifact.Policy{}, logger.Discard(), nil, nil)

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
	return json.NewEncoder(conn), json.NewDecoder(conn)
}

func TestLiveCurrentEmptyRingErrors(t *testing.T) {
	enc, dec := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())
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
	enc, dec := seededDaemon(t, ring, metrics.NewCollector())

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
	last := arr[1].(map[string]any)
	if last["id"] != "s3" {
		t.Fatalf("live.history last id = %v, want s3", last["id"])
	}
}

func TestProcessLiveReplaysSeededCollector(t *testing.T) {
	collector := metrics.NewCollector()
	now := time.Now()
	collector.Add(metrics.Sample{PID: 4242, TakenAt: now.Add(-2 * time.Second), RSSKiB: 100})
	collector.Add(metrics.Sample{PID: 4242, TakenAt: now.Add(-1 * time.Second), RSSKiB: 200})
	enc, dec := seededDaemon(t, livehistory.NewRing(4), collector)

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
	enc, dec := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())
	resp := rpcCall(t, enc, dec, 84, "process.history", `{}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("expected CodeInvalidParams for missing pid, got %+v", resp.Error)
	}
}

func TestProcessHistoryReturnsSliceForKnownPID(t *testing.T) {
	enc, dec := seededDaemon(t, livehistory.NewRing(4), metrics.NewCollector())
	// No aggregates written, so the DB returns an empty (but valid) result.
	resp := rpcCall(t, enc, dec, 85, "process.history", `{"pid":4242,"limit":10}`)
	if resp.Error != nil {
		t.Fatalf("process.history: %v", resp.Error)
	}
}
