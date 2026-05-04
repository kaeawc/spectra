package rpc

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandleMethodNotFound(t *testing.T) {
	d := NewDispatcher()
	resp := d.handle([]byte(`{"jsonrpc":"2.0","id":1,"method":"missing"}`))
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
}

func TestHandleParseError(t *testing.T) {
	d := NewDispatcher()
	resp := d.handle([]byte(`{bad json`))
	if resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Errorf("expected parse error, got %+v", resp)
	}
}

func TestHandleInvalidRequest(t *testing.T) {
	d := NewDispatcher()
	// Missing method field.
	resp := d.handle([]byte(`{"jsonrpc":"2.0","id":1}`))
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Errorf("expected invalid request, got %+v", resp)
	}
}

func TestHandleRegisteredMethod(t *testing.T) {
	d := NewDispatcher()
	d.Register("add", func(params json.RawMessage) (any, error) {
		var p struct{ A, B int }
		_ = json.Unmarshal(params, &p)
		return p.A + p.B, nil
	})
	resp := d.handle([]byte(`{"jsonrpc":"2.0","id":42,"method":"add","params":{"A":3,"B":4}}`))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	n, ok := resp.Result.(int)
	if !ok || n != 7 {
		t.Errorf("result = %v, want 7", resp.Result)
	}
	// ID must round-trip.
	if string(resp.ID) != "42" {
		t.Errorf("id = %s, want 42", resp.ID)
	}
}

func TestServeOverUnixSocket(t *testing.T) {
	d := NewDispatcher()
	d.Register("ping", func(_ json.RawMessage) (any, error) {
		return "pong", nil
	})

	sockPath := t.TempDir() + "/test.sock"
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go d.ServeListener(ln)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))

	req := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(buf[:n]))
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if resp.Result != "pong" {
		t.Errorf("result = %v, want pong", resp.Result)
	}
}
