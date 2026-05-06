package helper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestTCCQueryRejectsInvalidBundleID(t *testing.T) {
	d := NewDispatcher()
	RegisterAll(d, func(string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("should not be called")
	})

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.tcc.system.query","params":{"bundle_id":"x'; --"}}`)
	resp := d.handle(501, req)
	if resp.Error == nil {
		t.Fatal("expected invalid bundle_id error")
	}
}

func TestTCCQueryUsesAllowlistedBundleID(t *testing.T) {
	d := NewDispatcher()
	var gotName string
	var gotArgs []string
	RegisterAll(d, func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte("kTCCServiceCamera|2\n"), nil
	})

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.tcc.system.query","params":{"bundle_id":"com.example.App-1"}}`)
	resp := d.handle(501, req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if gotName != "sqlite3" {
		t.Fatalf("command = %q, want sqlite3", gotName)
	}
	if len(gotArgs) != 2 {
		t.Fatalf("args = %v, want db path and query", gotArgs)
	}
	if gotArgs[0] != "/Library/Application Support/com.apple.TCC/TCC.db" {
		t.Errorf("db path = %q", gotArgs[0])
	}
	wantQuery := `SELECT service, auth_value FROM access WHERE client="com.example.App-1"`
	if gotArgs[1] != wantQuery {
		t.Errorf("query = %q, want %q", gotArgs[1], wantQuery)
	}
}

func TestFirewallRulesUsesPFCTL(t *testing.T) {
	d := NewDispatcher()
	var gotName string
	var gotArgs []string
	RegisterAll(d, func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = args
		return []byte("block drop all\npass out all\n"), nil
	})

	req := []byte(`{"jsonrpc":"2.0","id":4,"method":"helper.firewall.rules"}`)
	resp := d.handle(501, req)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if gotName != "pfctl" || len(gotArgs) != 2 || gotArgs[0] != "-s" || gotArgs[1] != "rules" {
		t.Fatalf("command = %s %v, want pfctl -s rules", gotName, gotArgs)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["raw_rules"] != "block drop all\npass out all\n" {
		t.Errorf("raw_rules = %q", m["raw_rules"])
	}
}

type fakeFSUsageProcess struct {
	ctx context.Context
}

func (p fakeFSUsageProcess) Wait() error {
	<-p.ctx.Done()
	return p.ctx.Err()
}

func TestFSUsageStartRejectsInvalidParams(t *testing.T) {
	d := NewDispatcher()
	called := false
	registerAll(d, nil, func(context.Context, io.Writer, io.Writer, string, ...string) (fsUsageProcess, error) {
		called = true
		return nil, fmt.Errorf("should not be called")
	})

	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.fs_usage.start","params":{"mode":"filesys"}}`)
	resp := d.handle(501, req)
	if resp.Error == nil {
		t.Fatal("expected missing pid error")
	}
	if called {
		t.Fatal("fs_usage starter should not be called")
	}

	req = []byte(`{"jsonrpc":"2.0","id":2,"method":"helper.fs_usage.start","params":{"pid":42,"mode":"bad"}}`)
	resp = d.handle(501, req)
	if resp.Error == nil {
		t.Fatal("expected invalid mode error")
	}
	if called {
		t.Fatal("fs_usage starter should not be called")
	}
}

func TestFSUsageStartStop(t *testing.T) {
	d := NewDispatcher()
	var gotName string
	var gotArgs []string
	registerAll(d, nil, func(ctx context.Context, stdout, _ io.Writer, name string, args ...string) (fsUsageProcess, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		_, _ = io.WriteString(stdout, "open /tmp/example\n")
		return fakeFSUsageProcess{ctx: ctx}, nil
	})

	startReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"helper.fs_usage.start","params":{"pid":42,"mode":"pathname","duration_ms":1000}}`)
	startResp := d.handle(501, startReq)
	if startResp.Error != nil {
		t.Fatalf("start error: %+v", startResp.Error)
	}
	if gotName != "fs_usage" {
		t.Fatalf("command = %q, want fs_usage", gotName)
	}
	wantArgs := []string{"-w", "-f", "pathname", "42"}
	if fmt.Sprint(gotArgs) != fmt.Sprint(wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	startResult, ok := startResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("start result type %T, want map", startResp.Result)
	}
	handle, _ := startResult["handle"].(string)
	if handle == "" {
		t.Fatalf("handle = %q", handle)
	}

	stopReq := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"helper.fs_usage.stop","params":{"handle":%q}}`, handle))
	stopResp := d.handle(501, stopReq)
	if stopResp.Error != nil {
		t.Fatalf("stop error: %+v", stopResp.Error)
	}
	stopResult, ok := stopResp.Result.(map[string]any)
	if !ok {
		t.Fatalf("stop result type %T, want map", stopResp.Result)
	}
	if stopResult["raw_output"] != "open /tmp/example\n" {
		t.Fatalf("raw_output = %q", stopResult["raw_output"])
	}
	if stopResult["stopped"] != true {
		t.Fatalf("stopped = %v, want true", stopResult["stopped"])
	}
}
