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

func TestDaemonHealthEndpoint(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	dbPath := filepath.Join(dir, "test.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	d := rpc.NewDispatcher()
	registerHandlers(d, "test-version", db, metrics.NewCollector())

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go d.ServeListener(ln)

	// Give the listener a moment to start.
	time.Sleep(10 * time.Millisecond)

	conn, err := rpc.DialUnix(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	if err := enc.Encode(req{JSONRPC: "2.0", ID: 1, Method: "health"}); err != nil {
		t.Fatal(err)
	}

	conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(3 * time.Second))

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
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "test.sock")
	dbPath := filepath.Join(dir, "test.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	d := rpc.NewDispatcher()
	registerHandlers(d, "test", db, metrics.NewCollector())

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go d.ServeListener(ln)
	time.Sleep(10 * time.Millisecond)

	conn, err := rpc.DialUnix(sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(3 * time.Second))

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

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
	// Either is fine; just check it doesn't error.
}
