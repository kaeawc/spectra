package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// --- framing ---

func TestFramingRoundTrip(t *testing.T) {
	payload := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.health"}`)
	var buf bytes.Buffer
	if err := WriteMessage(&buf, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %q, want %q", got, payload)
	}
}

func TestFramingEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, []byte{}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFramingOversized(t *testing.T) {
	large := make([]byte, maxMessageSize+1)
	var buf bytes.Buffer
	if err := WriteMessage(&buf, large); err == nil {
		t.Error("expected error for oversized message")
	}
}

// --- dispatcher ---

func TestDispatcherHealth(t *testing.T) {
	d := NewDispatcher()
	RegisterAll(d, func(name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("unexpected command: %s", name)
	})

	sockPath := filepath.Join(t.TempDir(), "helper.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go d.ServeListener(ln)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "helper.health"}
	payload, _ := json.Marshal(req)
	if err := WriteMessage(conn, payload); err != nil {
		t.Fatal(err)
	}

	raw, err := ReadMessage(conn)
	if err != nil {
		t.Fatal(err)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
}

func TestDispatcherMethodNotFound(t *testing.T) {
	d := NewDispatcher()

	sockPath := filepath.Join(t.TempDir(), "helper.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go d.ServeListener(ln)

	conn, err := net.DialTimeout("unix", sockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "helper.unknown"}
	payload, _ := json.Marshal(req)
	WriteMessage(conn, payload)

	raw, err := ReadMessage(conn)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	json.Unmarshal(raw, &resp)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("expected method-not-found error, got %+v", resp)
	}
}

func TestTCCQueryRequiresBundleID(t *testing.T) {
	d := NewDispatcher()
	RegisterAll(d, func(string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("should not be called")
	})

	sockPath := filepath.Join(t.TempDir(), "helper.sock")
	ln, _ := net.Listen("unix", sockPath)
	defer ln.Close()
	go d.ServeListener(ln)

	conn, _ := net.DialTimeout("unix", sockPath, time.Second)
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))

	req := map[string]any{"jsonrpc": "2.0", "id": 3, "method": "helper.tcc.system.query",
		"params": map[string]any{}} // missing bundle_id
	payload, _ := json.Marshal(req)
	WriteMessage(conn, payload)

	raw, _ := ReadMessage(conn)
	var resp Response
	json.Unmarshal(raw, &resp)
	if resp.Error == nil {
		t.Error("expected error when bundle_id missing")
	}
}
