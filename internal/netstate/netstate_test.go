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
	found := map[int]ListeningPort{}
	for _, p := range ports {
		found[p.Port] = p
	}
	if _, ok := found[8080]; !ok {
		t.Errorf("ports = %+v, want 8080 and 9090", ports)
	}
	if _, ok := found[9090]; !ok {
		t.Errorf("ports = %+v, want 8080 and 9090", ports)
	}
	slack := found[8080]
	if slack.PID != 412 || slack.Command != "Slack" || slack.User != "alice" {
		t.Errorf("8080 attribution = %+v, want Slack pid=412 user=alice", slack)
	}
	if slack.LocalAddr != "*" || slack.Proto != "tcp" {
		t.Errorf("8080 endpoint = %+v, want tcp *:8080", slack)
	}
}

func TestParseLSOFListenInlineState(t *testing.T) {
	out := `COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
node    512 alice 12u IPv6 0x123 0t0 TCP [::1]:9229(LISTEN)
`
	ports := parseLSOFListen(out)
	if len(ports) != 1 {
		t.Fatalf("ports = %+v, want one inline LISTEN row", ports)
	}
	if ports[0].LocalAddr != "[::1]" || ports[0].Port != 9229 || ports[0].PID != 512 {
		t.Fatalf("inline listen parse = %+v", ports[0])
	}
}

func TestParseLSOFListenEmpty(t *testing.T) {
	ports := parseLSOFListen("")
	if len(ports) != 0 {
		t.Errorf("expected empty for blank input")
	}
}

const ifconfigWithVPN = `lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384
	inet 127.0.0.1 netmask 0xff000000
en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	inet 192.168.1.42 netmask 0xffffff00 broadcast 192.168.1.255
utun0: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1380
	inet6 fe80::1%utun0 prefixlen 64 scopeid 0xa
utun3: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1280
	inet 100.64.0.1 --> 100.64.0.1 netmask 0xffffffff
`

const ifconfigNoVPN = `lo0: flags=8049<UP,LOOPBACK,RUNNING,MULTICAST> mtu 16384
	inet 127.0.0.1 netmask 0xff000000
en0: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500
	inet 192.168.1.42 netmask 0xffffff00 broadcast 192.168.1.255
utun0: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1380
	inet6 fe80::1%utun0 prefixlen 64 scopeid 0xa
`

func TestParseVPNInterfacesActive(t *testing.T) {
	ifaces := parseVPNInterfaces(ifconfigWithVPN)
	if len(ifaces) != 1 || ifaces[0] != "utun3" {
		t.Errorf("got %v, want [utun3]", ifaces)
	}
}

func TestParseVPNInterfacesNoInet(t *testing.T) {
	// utun0 only has inet6, not inet — should not count.
	ifaces := parseVPNInterfaces(ifconfigNoVPN)
	if len(ifaces) != 0 {
		t.Errorf("got %v, want empty (no inet on utun)", ifaces)
	}
}

func TestParseVPNInterfacesEmpty(t *testing.T) {
	ifaces := parseVPNInterfaces("")
	if len(ifaces) != 0 {
		t.Errorf("got %v, want empty", ifaces)
	}
}

const nettopThroughputFixture = `,bytes_in,bytes_out,
Slack Helper.412,1000,2000,
Google Chrome H.999,3000,4000,
,bytes_in,bytes_out,
Slack Helper.412,7,11,
Google Chrome H.999,0,0,
bad-row,1,2,
`

func TestParseNettopThroughputUsesFinalSample(t *testing.T) {
	rows := parseNettopThroughput(nettopThroughputFixture)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	if rows[0].Command != "Slack Helper" || rows[0].PID != 412 {
		t.Fatalf("first row identity = %+v", rows[0])
	}
	if rows[0].BytesInPerSec != 7 || rows[0].BytesOutPerSec != 11 {
		t.Fatalf("first row throughput = %+v", rows[0])
	}
	if rows[1].Command != "Google Chrome H" || rows[1].PID != 999 {
		t.Fatalf("second row identity = %+v", rows[1])
	}
}

func TestCollectThroughputFiltersInactiveRows(t *testing.T) {
	var gotName string
	var gotArgs []string
	stub := func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return []byte(nettopThroughputFixture), nil
	}

	rows := CollectThroughput(stub)
	if gotName != "nettop" {
		t.Fatalf("command = %q, want nettop", gotName)
	}
	if !hasArg(gotArgs, "-d") || !hasArg(gotArgs, "-P") || !hasArg(gotArgs, "external") {
		t.Fatalf("nettop args = %v", gotArgs)
	}
	if len(rows) != 1 {
		t.Fatalf("active rows = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].PID != 412 {
		t.Fatalf("active row = %+v", rows[0])
	}
}

func hasArg(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
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
			if hasArg(args, "-sTCP:LISTEN") {
				return []byte(lsofOutput), nil
			}
			return []byte(lsofFixture), nil
		case "ifconfig":
			return []byte(ifconfigWithVPN), nil
		case "nettop":
			return []byte(nettopThroughputFixture), nil
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
	if !s.VPNActive {
		t.Error("VPNActive should be true")
	}
	if len(s.VPNInterfaces) != 1 || s.VPNInterfaces[0] != "utun3" {
		t.Errorf("VPNInterfaces = %v, want [utun3]", s.VPNInterfaces)
	}
	if s.EstablishedConnectionsCount != 1 {
		t.Errorf("EstablishedConnectionsCount = %d, want 1", s.EstablishedConnectionsCount)
	}
	if len(s.ProcessThroughput) != 1 || s.ProcessThroughput[0].PID != 412 {
		t.Errorf("ProcessThroughput = %+v, want active Slack row", s.ProcessThroughput)
	}
}

func TestCollectNoVPN(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "route", "scutil", "lsof":
			return []byte(""), nil
		case "ifconfig":
			return []byte(ifconfigNoVPN), nil
		}
		return nil, errors.New("unexpected command")
	}
	s := Collect(stub)
	if s.VPNActive {
		t.Error("VPNActive should be false when no utun has inet")
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
	if s.VPNActive {
		t.Error("VPNActive should be false when ifconfig fails")
	}
}

func TestCollectConnectionsCountFallsBackToNetstat(t *testing.T) {
	stub := func(name string, args ...string) ([]byte, error) {
		switch name {
		case "route", "scutil", "ifconfig":
			return []byte(""), nil
		case "lsof":
			return nil, errors.New("lsof failed")
		case "netstat":
			return []byte(netstatFixture), nil
		}
		return nil, errors.New("unexpected command")
	}
	s := Collect(stub)
	if s.EstablishedConnectionsCount != 1 {
		t.Errorf("EstablishedConnectionsCount = %d, want 1", s.EstablishedConnectionsCount)
	}
}
