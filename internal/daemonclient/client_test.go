package daemonclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
)

func TestClientConcurrentCallsAndNotification(t *testing.T) {
	// A short /tmp path stays within the 104-byte Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	s := daemon.New(daemon.Options{Paths: p})
	s.Register("test.echo", func(_ context.Context, r *daemon.Request) (any, error) {
		var n int
		if err := json.Unmarshal(r.Params, &n); err != nil {
			return nil, daemon.InvalidParams("number required")
		}
		if n == 0 {
			if err := r.Notify("test.event", map[string]int{"n": n}); err != nil {
				return nil, err
			}
		}
		return n, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var c *Client
	for i := 0; i < 100; i++ {
		c, err = Dial(context.Background(), p.Socket)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	notified := make(chan string, 1)
	c.OnNotification(func(method string, _ json.RawMessage) { notified <- method })
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			var got int
			if err := c.Call(context.Background(), "test.echo", n, &got); err != nil || got != n {
				t.Errorf("call %d: %d, %v", n, got, err)
			}
		}(i)
	}
	wg.Wait()
	select {
	case method := <-notified:
		if method != "test.event" {
			t.Fatal(method)
		}
	case <-time.After(time.Second):
		t.Fatal("notification missing")
	}
	var out any
	err = c.Call(context.Background(), "missing", nil, &out)
	var rpc *RPCError
	if !errors.As(err, &rpc) || rpc.Code != -32601 {
		t.Fatalf("unknown method: %v", err)
	}
	st, err := c.Status(context.Background())
	if err != nil || st.Protocol != daemon.ProtocolVersion {
		t.Fatalf("status: %+v, %v", st, err)
	}
	cancel()
	<-done
}

func TestCheckVersion(t *testing.T) {
	for _, tc := range []struct {
		st       daemon.StatusResult
		client   string
		mismatch bool
	}{
		{daemon.StatusResult{Protocol: daemon.ProtocolVersion, Version: "1"}, "1", false},
		{daemon.StatusResult{Protocol: daemon.ProtocolVersion, Version: "dev"}, "2", false},
		{daemon.StatusResult{Protocol: daemon.ProtocolVersion, Version: "1"}, "2", true},
		{daemon.StatusResult{Protocol: "wrong", Version: "1"}, "1", true},
	} {
		err := CheckVersion(tc.st, tc.client)
		if (err != nil) != tc.mismatch {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestClientNotificationFIFO(t *testing.T) {
	// A short /tmp path stays within the 104-byte Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	s := daemon.New(daemon.Options{Paths: p})
	s.Register("test.burst", func(_ context.Context, r *daemon.Request) (any, error) {
		for i := 0; i < 2000; i++ {
			if err := r.Notify("test.number", i); err != nil {
				return nil, err
			}
		}
		return true, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var c *Client
	for i := 0; i < 100; i++ {
		c, err = Dial(context.Background(), p.Socket)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var mu sync.Mutex
	got := make([]int, 0, 2000)
	all := make(chan struct{})
	c.OnNotification(func(_ string, raw json.RawMessage) {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			t.Errorf("notification: %v", err)
			return
		}
		mu.Lock()
		got = append(got, n)
		if len(got) == 2000 {
			close(all)
		}
		mu.Unlock()
	})
	var result bool
	if err := c.Call(context.Background(), "test.burst", nil, &result); err != nil || !result {
		t.Fatalf("burst: %v, %v", result, err)
	}
	select {
	case <-all:
	case <-time.After(5 * time.Second):
		t.Fatal("notification queue did not drain")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, n := range got {
		if n != i {
			t.Fatalf("notification %d = %d", i, n)
		}
	}
	cancel()
	<-done
}
