package serve

import (
	"context"
	"encoding/json"
	"net"
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
	dir := t.TempDir()
	// Use a short socket name: macOS limits Unix socket paths to 104 bytes.
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
