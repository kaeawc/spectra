package serve

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	registerHandlers(d, "test-version", db, metrics.NewCollector())

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
