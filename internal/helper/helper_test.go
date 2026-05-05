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

func TestDispatcherAuditLogSuccessAndFailure(t *testing.T) {
	d := NewDispatcher()
	var log bytes.Buffer
	d.SetAuditWriter(&log)
	base := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	tick := 0
	d.SetClock(func() time.Time {
		tick++
		return base.Add(time.Duration(tick) * time.Millisecond)
	})
	d.Register("helper.ok", func(uid uint32, _ json.RawMessage) (any, error) {
		if uid != 501 {
			t.Fatalf("uid = %d, want 501", uid)
		}
		return map[string]any{"ok": true}, nil
	})

	okReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.ok"}`)
	if resp := d.handle(501, okReq); resp.Error != nil {
		t.Fatalf("unexpected response error: %+v", resp.Error)
	}
	badReq := []byte(`{"jsonrpc":"2.0","id":2,"method":"helper.missing"}`)
	if resp := d.handle(501, badReq); resp.Error == nil {
		t.Fatal("expected missing method error")
	}

	lines := bytes.Split(bytes.TrimSpace(log.Bytes()), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want 2: %s", len(lines), log.String())
	}
	var first, second auditEvent
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatal(err)
	}
	if !first.OK || first.Method != "helper.ok" || first.UID != 501 {
		t.Errorf("first audit event = %+v", first)
	}
	if second.OK || second.Method != "helper.missing" || second.Error != "method not found" {
		t.Errorf("second audit event = %+v", second)
	}
}

func TestDispatcherRateLimitsPerUID(t *testing.T) {
	d := NewDispatcher()
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	d.SetClock(func() time.Time { return now })
	d.SetRateLimit(2, time.Minute)
	d.Register("helper.ok", func(uint32, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.ok"}`)

	if resp := d.handle(501, req); resp.Error != nil {
		t.Fatalf("first request error: %+v", resp.Error)
	}
	if resp := d.handle(501, req); resp.Error != nil {
		t.Fatalf("second request error: %+v", resp.Error)
	}
	if resp := d.handle(502, req); resp.Error != nil {
		t.Fatalf("different UID should not be limited: %+v", resp.Error)
	}
	if resp := d.handle(501, req); resp.Error == nil || resp.Error.Code != -32001 {
		t.Fatalf("third request error = %+v, want rate limit", resp.Error)
	}

	now = now.Add(time.Minute + time.Millisecond)
	if resp := d.handle(501, req); resp.Error != nil {
		t.Fatalf("request after window error: %+v", resp.Error)
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
