package main

import (
	"bytes"
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

func TestParseConnectTypedCalls(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMethod string
		wantParams string
	}{
		{name: "inspect app", args: []string{"inspect", "/Applications/Slack.app"}, wantMethod: "inspect.app", wantParams: `{"path":"/Applications/Slack.app"}`},
		{name: "host", args: []string{"host"}, wantMethod: "inspect.host"},
		{name: "jvm list", args: []string{"jvm"}, wantMethod: "jvm.list"},
		{name: "jvm inspect", args: []string{"jvm", "4012"}, wantMethod: "jvm.inspect", wantParams: `{"pid":4012}`},
		{name: "jvm gc", args: []string{"jvm-gc", "4012"}, wantMethod: "jvm.gc_stats", wantParams: `{"pid":4012}`},
		{name: "jvm threads", args: []string{"jvm-threads", "4012"}, wantMethod: "jvm.thread_dump", wantParams: `{"pid":4012}`},
		{name: "jvm heap", args: []string{"jvm-heap", "4012"}, wantMethod: "jvm.heap_histogram", wantParams: `{"pid":4012}`},
		{name: "processes", args: []string{"processes"}, wantMethod: "process.list"},
		{name: "processes scoped", args: []string{"processes", "/Applications/Slack.app"}, wantMethod: "process.list", wantParams: `{"bundles":["/Applications/Slack.app"]}`},
		{name: "process tree", args: []string{"process-tree"}, wantMethod: "process.tree"},
		{name: "sample", args: []string{"sample", "4012", "2", "20"}, wantMethod: "process.sample", wantParams: `{"duration":2,"interval":20,"pid":4012}`},
		{name: "metrics", args: []string{"metrics"}, wantMethod: "process.live"},
		{name: "metrics pid", args: []string{"metrics", "4012"}, wantMethod: "process.history", wantParams: `{"pid":4012}`},
		{name: "metrics pid and limit", args: []string{"metrics", "4012", "30"}, wantMethod: "process.history", wantParams: `{"pid":4012,"limit":30}`},
		{name: "network", args: []string{"network"}, wantMethod: "network.state"},
		{name: "connections", args: []string{"connections"}, wantMethod: "network.connections"},
		{name: "network by app", args: []string{"network-by-app", "/Applications/Slack.app"}, wantMethod: "network.byApp", wantParams: `{"bundles":["/Applications/Slack.app"]}`},
		{name: "storage system", args: []string{"storage"}, wantMethod: "storage.system"},
		{name: "storage by app", args: []string{"storage", "/Applications/Slack.app", "/Applications/Cursor.app"}, wantMethod: "storage.byApp", wantParams: `{"paths":["/Applications/Slack.app","/Applications/Cursor.app"]}`},
		{name: "power", args: []string{"power"}, wantMethod: "power.state"},
		{name: "rules", args: []string{"rules"}, wantMethod: "rules.check"},
		{name: "rules snapshot", args: []string{"rules", "snap-1"}, wantMethod: "rules.check", wantParams: `{"snapshot_id":"snap-1"}`},
		{name: "jdk", args: []string{"jdk"}, wantMethod: "jdk.list"},
		{name: "brew", args: []string{"brew"}, wantMethod: "toolchain.brew"},
		{name: "runtimes", args: []string{"runtimes"}, wantMethod: "toolchain.runtimes"},
		{name: "build tools", args: []string{"build-tools"}, wantMethod: "toolchain.build_tools"},
		{name: "toolchains", args: []string{"toolchains"}, wantMethod: "toolchain.scan"},
		{name: "snapshot create", args: []string{"snapshot"}, wantMethod: "snapshot.create"},
		{name: "snapshot list", args: []string{"snapshot", "list"}, wantMethod: "snapshot.list"},
		{name: "snapshots alias", args: []string{"snapshots"}, wantMethod: "snapshot.list"},
		{name: "snapshot get", args: []string{"snapshot", "get", "snap-1"}, wantMethod: "snapshot.get", wantParams: `{"ID":"snap-1"}`},
		{name: "snapshot diff", args: []string{"snapshot", "diff", "snap-a", "snap-b"}, wantMethod: "snapshot.diff", wantParams: `{"id_a":"snap-a","id_b":"snap-b"}`},
		{name: "snapshot processes", args: []string{"snapshot", "processes", "snap-1"}, wantMethod: "snapshot.processes", wantParams: `{"id":"snap-1"}`},
		{name: "snapshot login items", args: []string{"snapshot", "login-items", "snap-1"}, wantMethod: "snapshot.login_items", wantParams: `{"id":"snap-1"}`},
		{name: "snapshot granted perms", args: []string{"snapshot", "granted-perms", "snap-1"}, wantMethod: "snapshot.granted_perms", wantParams: `{"id":"snap-1"}`},
		{name: "snapshot prune default", args: []string{"snapshot", "prune"}, wantMethod: "snapshot.prune"},
		{name: "snapshot prune keep", args: []string{"snapshot", "prune", "25"}, wantMethod: "snapshot.prune", wantParams: `{"keep":25}`},
		{name: "cache", args: []string{"cache"}, wantMethod: "cache.stats"},
		{name: "cache stats", args: []string{"cache", "stats"}, wantMethod: "cache.stats"},
		{name: "cache clear", args: []string{"cache", "clear"}, wantMethod: "cache.clear"},
		{name: "cache clear kind", args: []string{"cache", "clear", "detect"}, wantMethod: "cache.clear", wantParams: `{"kind":"detect"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			method, params, err := parseConnectCall(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if method != tt.wantMethod {
				t.Fatalf("method = %q, want %q", method, tt.wantMethod)
			}
			if tt.wantParams == "" && params != nil {
				t.Fatalf("params = %s, want nil", string(params))
			}
			if tt.wantParams != "" {
				assertJSONEqual(t, params, tt.wantParams)
			}
		})
	}
}

func TestParseConnectTypedCallErrors(t *testing.T) {
	tests := [][]string{
		{"inspect"},
		{"jvm", "nope"},
		{"jvm-gc"},
		{"jvm-threads", "0"},
		{"sample", "4012", "0"},
		{"metrics", "0"},
		{"metrics", "4012", "0"},
		{"metrics", "4012", "30", "extra"},
		{"rules", "snap-1", "extra"},
		{"snapshot", "get"},
		{"snapshot", "diff", "snap-a"},
		{"snapshot", "processes"},
		{"snapshot", "prune", "-1"},
		{"snapshot", "unknown"},
		{"host", "extra"},
		{"status", "extra"},
		{"health", "extra"},
		{"cache", "clear", "too", "many"},
		{"cache", "weird"},
	}
	for _, args := range tests {
		t.Run(args[0], func(t *testing.T) {
			if _, _, err := parseConnectCall(args); err == nil {
				t.Fatalf("parseConnectCall(%v) succeeded, want error", args)
			}
		})
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

func assertJSONEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got params: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode want params: %v", err)
	}
	gotCanonical, err := json.Marshal(gotValue)
	if err != nil {
		t.Fatalf("encode got params: %v", err)
	}
	wantCanonical, err := json.Marshal(wantValue)
	if err != nil {
		t.Fatalf("encode want params: %v", err)
	}
	if !bytes.Equal(gotCanonical, wantCanonical) {
		t.Fatalf("params = %s, want %s", string(got), want)
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
