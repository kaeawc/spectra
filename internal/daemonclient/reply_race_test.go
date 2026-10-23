package daemonclient

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A server that answers and immediately hangs up (the shutdown RPC) must not
// turn a delivered reply into a "use of closed network connection" error.
func TestCallPrefersReplyOverConcurrentClose(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
					return
				}
				_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
			}(conn)
		}
	}()
	for i := 0; i < 2000; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, err := Dial(ctx, socket)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var res struct {
			OK bool `json:"ok"`
		}
		err = c.Call(ctx, "daemon.shutdown", nil, &res)
		c.Close()
		cancel()
		if err != nil || !res.OK {
			t.Fatalf("iteration %d: %+v, %v", i, res, err)
		}
	}
}
