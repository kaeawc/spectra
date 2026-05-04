package serve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/store"
)

// testDaemon wires up a dispatcher with registered handlers, starts it on a
// temp Unix socket, and returns a connected RPC client.
func testDaemon(t *testing.T) (*json.Encoder, *json.Decoder, context.CancelFunc) {
	t.Helper()
	// Use os.MkdirTemp with a short prefix under /tmp: macOS limits Unix socket
	// paths to 104 bytes and t.TempDir() embeds the full test name which can
	// exceed that limit for long test names.
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	dbPath := filepath.Join(dir, "t.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	d := rpc.NewDispatcher()
	registerHandlers(d, "test-version", db, metrics.NewCollector(), cache.Default, nil)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ctx.Done(); ln.Close() }()
	go d.ServeListener(ln)
	time.Sleep(10 * time.Millisecond)

	conn, err := rpc.DialUnix(sockPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { conn.Close() })

	return json.NewEncoder(conn), json.NewDecoder(conn), cancel
}

func TestDaemonHealthEndpoint(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	if err := enc.Encode(req{JSONRPC: "2.0", ID: 1, Method: "health"}); err != nil {
		t.Fatal(err)
	}
	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
	if m["version"] != "test-version" {
		t.Errorf("version = %v, want test-version", m["version"])
	}
}

func TestDaemonSnapshotList(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 2, Method: "snapshot.list"})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// Empty DB → result should be nil or empty slice.
}

func TestDaemonInspectAppMissingPath(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Missing path → should return an RPC error, not crash.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 3, Method: "inspect.app", Params: json.RawMessage(`{}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for missing path, got nil")
	}
}

func TestDaemonInspectAppBatchEmpty(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Empty paths → should return an RPC error.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 4, Method: "inspect.app.batch", Params: json.RawMessage(`{"paths":[]}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for empty paths, got nil")
	}
}

func TestDaemonInspectHost(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 5, Method: "inspect.host"})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	// HostInfo should have at least a hostname field.
	if m["hostname"] == nil && m["Hostname"] == nil {
		t.Error("inspect.host: expected hostname in result")
	}
}

func rpcCall(t *testing.T, enc *json.Encoder, dec *json.Decoder, id int, method string, params string) rpc.Response {
	t.Helper()
	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: id, Method: method, Params: json.RawMessage(params)})
	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("rpcCall %s: decode: %v", method, err)
	}
	return resp
}

func TestDaemonIssuesListMissingMachine(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 10, "issues.list", `{}`)
	if resp.Error == nil {
		t.Error("expected error when machine_uuid missing")
	}
}

func TestDaemonIssuesRecordEmpty(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	// issues.record with empty findings should succeed (0 upserts).
	resp := rpcCall(t, enc, dec, 11, "issues.record",
		`{"machine_uuid":"TEST-1","snapshot_id":"snap-X","findings":[]}`)
	if resp.Error != nil {
		t.Fatalf("issues.record (empty): %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", resp.Result)
	}
	if m["upserted"].(float64) != 0 {
		t.Errorf("upserted = %v, want 0", m["upserted"])
	}
}

func TestDaemonIssuesUpdateMissingStatus(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 12, "issues.update", `{"id":"x"}`)
	if resp.Error == nil {
		t.Error("expected error when status missing")
	}
}

func TestDaemonIssuesFixRecordMissingIssueID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 13, "issues.fix.record", `{}`)
	if resp.Error == nil {
		t.Error("expected error when issue_id missing")
	}
}

func TestDaemonIssuesFixListMissingIssueID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 14, "issues.fix.list", `{}`)
	if resp.Error == nil {
		t.Error("expected error when issue_id missing")
	}
}

func TestDaemonJDKListReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 16, "jdk.list", `{}`)
	if resp.Error != nil {
		t.Fatalf("jdk.list: %v", resp.Error)
	}
}

func TestDaemonToolchainScanReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 17, "toolchain.scan", `{}`)
	if resp.Error != nil {
		t.Fatalf("toolchain.scan: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["brew"] == nil {
		t.Error("toolchain.scan: expected brew field in result")
	}
}

func TestDaemonSnapshotDiffMissingIDs(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Missing id_b → should return an RPC error.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 6, Method: "snapshot.diff", Params: json.RawMessage(`{"id_a":"x"}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for missing id_b, got nil")
	}
}

func TestDaemonNetworkStateReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 18, "network.state", `{}`)
	if resp.Error != nil {
		t.Fatalf("network.state: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	// network.state should have vpn_active field.
	if _, exists := m["vpn_active"]; !exists {
		t.Error("network.state: expected vpn_active field in result")
	}
}

func TestDaemonProcessListReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 19, "process.list", `{}`)
	if resp.Error != nil {
		t.Fatalf("process.list: %v", resp.Error)
	}
	// Result should be a JSON array (slice).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

// storage.system is deliberately not tested here: it walks ~/Library which
// can take 10+ seconds, exceeding the 5-second test socket deadline. It is
// tested via storagestate_test.go instead.

func TestDaemonStorageByAppEmptyPaths(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 21, "storage.byApp", `{"paths":[]}`)
	if resp.Error == nil {
		t.Error("expected error for empty paths")
	}
}

func TestDaemonProcessTreeReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 22, "process.tree", `{}`)
	if resp.Error != nil {
		t.Fatalf("process.tree: %v", resp.Error)
	}
	// Result should be a JSON array (slice of tree roots).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

func TestDaemonJVMThreadDumpMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 30, "jvm.thread_dump", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMHeapHistogramMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 31, "jvm.heap_histogram", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMGCStatsMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 32, "jvm.gc_stats", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJFRStartMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 33, "jvm.jfr.start", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJFRStopMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 34, "jvm.jfr.stop", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonCacheStatsReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 40, "cache.stats", `{}`)
	if resp.Error != nil {
		t.Fatalf("cache.stats: %v", resp.Error)
	}
	// Result should be a slice (may be empty if no cache kinds registered in tests).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

func TestDaemonCacheClearAll(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 41, "cache.clear", `{}`)
	if resp.Error != nil {
		t.Fatalf("cache.clear (all): %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["cleared"] != "all" {
		t.Errorf("cleared = %v, want all", m["cleared"])
	}
}

func TestDaemonCacheClearUnknownKindReturnsError(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// "detect" kind is not registered in the test registry (no initCacheStores),
	// so clearing it should return an RPC error.
	resp := rpcCall(t, enc, dec, 42, "cache.clear", `{"kind":"detect"}`)
	if resp.Error == nil {
		t.Error("expected error for unregistered cache kind")
	}
}

func TestDaemonJVMInspectRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 50, "jvm.inspect", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonNetworkByAppReturnsMap(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 51, "network.byApp", `{}`)
	if resp.Error != nil {
		t.Fatalf("network.byApp: %v", resp.Error)
	}
	// Result should be a map (may be empty if no connections, but must be a map).
	if _, ok := resp.Result.(map[string]any); !ok {
		t.Fatalf("network.byApp: result type %T, want map", resp.Result)
	}
}

func TestDaemonJVMHeapDumpRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 52, "jvm.heap_dump", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonProcessSampleRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 53, "process.sample", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonHelperHealthWhenUnavailable(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// Helper is not running in tests; should return ok=false, not an error.
	resp := rpcCall(t, enc, dec, 54, "helper.health", `{}`)
	if resp.Error != nil {
		t.Fatalf("helper.health: unexpected RPC error: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("helper.health: result type %T, want map", resp.Result)
	}
	if m["helper"] != false {
		t.Errorf("helper.health: helper field = %v, want false", m["helper"])
	}
}

func TestDaemonHelperPowermetricsRequiresHelper(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// Helper not running → should return an error (not a silent no-op).
	resp := rpcCall(t, enc, dec, 55, "helper.powermetrics.sample", `{}`)
	if resp.Error == nil {
		t.Error("expected error when helper not running")
	}
}

func TestDaemonHelperTCCRequiresBundleID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 56, "helper.tcc.system.query", `{}`)
	if resp.Error == nil {
		t.Error("expected error when bundle_id missing")
	}
}

func TestDaemonSnapshotProcessesRequiresID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 57, "snapshot.processes", `{}`)
	if resp.Error == nil {
		t.Error("expected error when id missing")
	}
}
