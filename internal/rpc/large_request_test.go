package rpc

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// TestServeHandlesRequestLargerThan64KiB guards the fix for the silent 64 KiB
// request cap: a default bufio.Scanner would stop reading and drop the
// connection when a single request line exceeded 64 KiB. jsonrpc.ReadMessage
// grows to the line length, so a large payload round-trips normally.
func TestServeHandlesRequestLargerThan64KiB(t *testing.T) {
	d := NewDispatcher()
	d.Register("echo_len", func(params json.RawMessage) (any, error) {
		var p struct {
			Blob string `json:"blob"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return len(p.Blob), nil
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go d.ServeListener(ln)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	const blobLen = 256 * 1024 // 256 KiB, well past the old 64 KiB scanner cap
	blob := strings.Repeat("a", blobLen)
	req, err := json.Marshal(Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "echo_len",
		Params:  json.RawMessage(`{"blob":"` + blob + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	got, ok := resp.Result.(float64) // JSON numbers decode to float64
	if !ok || int(got) != blobLen {
		t.Fatalf("result = %v, want %d", resp.Result, blobLen)
	}
}
