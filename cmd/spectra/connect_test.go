package main

import (
	"encoding/json"
	"net"
	"testing"

	"github.com/kaeawc/spectra/internal/rpc"
)

func TestParseConnectTarget(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want connectTarget
	}{
		{
			name: "unix prefix",
			raw:  "unix:/tmp/spectra.sock",
			want: connectTarget{Network: "unix", Address: "/tmp/spectra.sock"},
		},
		{
			name: "absolute path",
			raw:  "/tmp/spectra.sock",
			want: connectTarget{Network: "unix", Address: "/tmp/spectra.sock"},
		},
		{
			name: "host port",
			raw:  "work-mac:9000",
			want: connectTarget{Network: "tcp", Address: "work-mac:9000"},
		},
		{
			name: "host default port",
			raw:  "work-mac",
			want: connectTarget{Network: "tcp", Address: "work-mac:7878"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseConnectTarget(tt.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("target = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseConnectCall(t *testing.T) {
	method, params, err := parseConnectCall([]string{"call", "inspect.app", `{"path":"/Applications/Slack.app"}`})
	if err != nil {
		t.Fatal(err)
	}
	if method != "inspect.app" {
		t.Fatalf("method = %q", method)
	}
	var decoded map[string]string
	if err := json.Unmarshal(params, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["path"] != "/Applications/Slack.app" {
		t.Fatalf("path = %q", decoded["path"])
	}

	method, params, err = parseConnectCall(nil)
	if err != nil {
		t.Fatal(err)
	}
	if method != "health" || params != nil {
		t.Fatalf("default call = %q %s, want health nil", method, string(params))
	}
}

func TestCallRPC(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()

	d := rpc.NewDispatcher()
	d.Register("echo", func(params json.RawMessage) (any, error) {
		var p map[string]string
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return p, nil
	})
	go d.Serve(server)

	got, err := callRPC(client, "echo", json.RawMessage(`{"value":"ok"}`))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["value"] != "ok" {
		t.Fatalf("value = %q, want ok", decoded["value"])
	}
}

func TestLoopbackListenAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:7878", "[::1]:7878", "localhost:7878"} {
		if !isLoopbackListenAddr(addr) {
			t.Fatalf("%s should be loopback", addr)
		}
	}
	for _, addr := range []string{":7878", "0.0.0.0:7878", "100.64.0.5:7878"} {
		if isLoopbackListenAddr(addr) {
			t.Fatalf("%s should not be loopback", addr)
		}
	}
}
