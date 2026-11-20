package helperclient

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/helper"
)

// serveOnce answers one request with resp and reports the request it got.
func serveOnce(t *testing.T, resp string) (*Client, chan map[string]any) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hc") // short path: macOS caps socket paths at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan map[string]any, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		raw, err := helper.ReadMessage(conn)
		if err != nil {
			return
		}
		var req map[string]any
		_ = json.Unmarshal(raw, &req)
		got <- req
		_ = helper.WriteMessage(conn, []byte(resp))
	}()
	return NewWithPath(sock), got
}

func TestLaunchdSystemPrepareSendsLabelAndAction(t *testing.T) {
	c, got := serveOnce(t, `{"jsonrpc":"2.0","id":1,"result":{"label":"com.example.d","action":"disable","refused":false,"token":"t","planned":[["/bin/launchctl","disable","system/com.example.d"]]}}`)
	res, err := c.LaunchdSystemPrepare("com.example.d", "disable")
	if err != nil || res.Token != "t" || len(res.Planned) != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	req := <-got
	params := req["params"].(map[string]any)
	if req["method"] != helper.MethodLaunchdPrepare || params["label"] != "com.example.d" || params["action"] != "disable" {
		t.Fatalf("request = %v", req)
	}
}

func TestLaunchdSystemChangeSendsOnlyToken(t *testing.T) {
	c, got := serveOnce(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"too many changes"}}`)
	_, err := c.LaunchdSystemChange("enable", "tok")
	if !IsRateLimited(err) {
		t.Fatalf("err = %v", err)
	}
	req := <-got
	params := req["params"].(map[string]any)
	if req["method"] != helper.MethodLaunchdEnable || len(params) != 1 || params["token"] != "tok" {
		t.Fatalf("request = %v", req)
	}
	if _, err := c.LaunchdSystemChange("kickstart", "tok"); err == nil {
		t.Fatal("unknown action accepted")
	}
}
