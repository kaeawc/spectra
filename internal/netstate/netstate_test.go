package netstate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseRoute(t *testing.T) {
	out := `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`
	iface, gw := parseRoute(out)
	if iface != "en0" {
		t.Errorf("iface = %q, want en0", iface)
	}
	if gw != "192.168.1.1" {
		t.Errorf("gw = %q, want 192.168.1.1", gw)
	}
}

func TestParseDNS(t *testing.T) {
	out := `DNS configuration

resolver #1
  nameserver[0] : 8.8.8.8
  nameserver[1] : 8.8.4.4
  flags    : Request A records, Request AAAA records
  reach    : 0x00000002 (Reachable)

resolver #2
  nameserver[0] : 8.8.8.8
  flags    : ...
`
	dns := parseDNS(out)
	// 8.8.8.8 appears twice but should be deduped.
	if len(dns) != 2 {
		t.Fatalf("dns = %v, want [8.8.8.8 8.8.4.4]", dns)
	}
	if dns[0] != "8.8.8.8" || dns[1] != "8.8.4.4" {
		t.Errorf("dns = %v", dns)
	}
}

func TestParseProxy(t *testing.T) {
	out := `<dictionary> {
  HTTPEnable : 1
  HTTPProxy : proxy.example.com
  HTTPPort : 8080
  HTTPSEnable : 0
  SOCKSEnable : 0
}`
	pc := parseProxy(out)
	if pc.HTTP != "proxy.example.com:8080" {
		t.Errorf("HTTP = %q, want proxy.example.com:8080", pc.HTTP)
	}
	if pc.HTTPS != "" {
		t.Errorf("HTTPS should be empty (disabled)")
	}
}

func TestParseProxySOCKS(t *testing.T) {
	out := `SOCKSEnable : 1
SOCKSProxy : socks.example.com
SOCKSPort : 1080
`
	pc := parseProxy(out)
	if pc.SOCKS != "socks.example.com:1080" {
		t.Errorf("SOCKS = %q, want socks.example.com:1080", pc.SOCKS)
	}
}

func TestReadHostsOverrides(t *testing.T) {
	content := `# default /etc/hosts
127.0.0.1       localhost
::1             localhost
255.255.255.255 broadcasthost

# custom entries
10.0.0.5        internal.corp.example.com
192.168.50.10   dev-db.local devdb
`
	path := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(path, []byte(content), 0o644)

	entries := readHostsOverrides(path)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].IP != "10.0.0.5" {
		t.Errorf("entries[0].IP = %q", entries[0].IP)
	}
	if entries[1].IP != "192.168.50.10" {
		t.Errorf("entries[1].IP = %q", entries[1].IP)
	}
	if len(entries[1].Names) != 2 {
		t.Errorf("entries[1] names = %v, want 2", entries[1].Names)
	}
}

func TestReadHostsOverridesMissing(t *testing.T) {
	entries := readHostsOverrides("/nonexistent/hosts")
	if entries != nil {
		t.Error("expected nil for missing hosts file")
	}
}

const lsofOutput = `COMMAND    PID   USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
Slack      412   alice  29u  IPv4  12345      0t0  TCP *:8080 (LISTEN)
Google     999   alice  12u  IPv4  99999      0t0  TCP *:9090 (LISTEN)
Slack      412   alice  30u  IPv4  12346      0t0  TCP *:8080 (LISTEN)
`

func TestParseLSOFListen(t *testing.T) {
	ports := parseLSOFListen(lsofOutput)
	if len(ports) != 2 {
		t.Fatalf("got %d ports, want 2 (8080 and 9090, deduped): %+v", len(ports), ports)
	}
	found := map[int]bool{}
	for _, p := range ports {
		found[p.Port] = true
	}
	if !found[8080] || !found[9090] {
		t.Errorf("ports = %+v, want 8080 and 9090", ports)
	}
}

func TestParseLSOFListenEmpty(t *testing.T) {
	ports := parseLSOFListen("")
	if len(ports) != 0 {
		t.Errorf("expected empty for blank input")
	}
}

func TestCollect(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "route":
			return []byte("interface: en0\ngateway: 10.0.0.1\n"), nil
		case "scutil":
			if len(args) > 0 && args[0] == "--dns" {
				return []byte("nameserver[0] : 1.1.1.1\n"), nil
			}
			return []byte("HTTPEnable : 0\n"), nil
		case "lsof":
			return []byte(lsofOutput), nil
		}
		return nil, errors.New("unexpected command")
	}

	s := Collect(stub)
	if s.DefaultRouteIface != "en0" {
		t.Errorf("DefaultRouteIface = %q", s.DefaultRouteIface)
	}
	if len(s.DNSServers) != 1 || s.DNSServers[0] != "1.1.1.1" {
		t.Errorf("DNSServers = %v", s.DNSServers)
	}
	if len(s.ListeningPorts) != 2 {
		t.Errorf("ListeningPorts = %d", len(s.ListeningPorts))
	}
}

func TestCollectAllFail(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		return nil, errors.New("not available")
	}
	s := Collect(stub)
	if s.DefaultRouteIface != "" || len(s.DNSServers) != 0 {
		t.Errorf("expected zero value on all-fail: %+v", s)
	}
}
