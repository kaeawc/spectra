package netstate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestParseProcNetRoute(t *testing.T) {
	// Gateway 0102A8C0 little-endian = 192.168.2.1; default row has
	// Destination 00000000. A non-default row precedes it.
	data := []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"eth0\t0000FEA9\t00000000\t0001\t0\t0\t1000\t0000FFFF\n" +
		"eth0\t00000000\t0102A8C0\t0003\t0\t0\t100\t00000000\n")
	iface, gw := parseProcNetRoute(data)
	if iface != "eth0" {
		t.Errorf("iface = %q, want eth0", iface)
	}
	if gw != "192.168.2.1" {
		t.Errorf("gw = %q, want 192.168.2.1", gw)
	}
}

func TestParseResolvConf(t *testing.T) {
	dns := parseResolvConf([]byte("# comment\nnameserver 1.1.1.1\nsearch lan\nnameserver 8.8.8.8\nnameserver 1.1.1.1\n"))
	if len(dns) != 2 || dns[0] != "1.1.1.1" || dns[1] != "8.8.8.8" {
		t.Errorf("parseResolvConf = %v, want [1.1.1.1 8.8.8.8] (deduped)", dns)
	}
}

func TestParseSSListen(t *testing.T) {
	out := `State  Recv-Q Send-Q Local Address:Port Peer Address:Port Process
LISTEN 0      128    0.0.0.0:22         0.0.0.0:*         users:(("sshd",pid=789,fd=3))
LISTEN 0      4096   [::]:443           [::]:*            users:(("nginx",pid=1011,fd=6))
`
	ports := parseSSListen(out, "tcp")
	if len(ports) != 2 {
		t.Fatalf("got %d ports, want 2: %+v", len(ports), ports)
	}
	if ports[0].Port != 22 || ports[0].Command != "sshd" || ports[0].PID != 789 {
		t.Errorf("port[0] = %+v, want :22 sshd pid 789", ports[0])
	}
	if ports[0].Proto != "tcp" {
		t.Errorf("proto = %q, want tcp", ports[0].Proto)
	}
	if ports[1].Port != 443 || ports[1].Command != "nginx" {
		t.Errorf("port[1] = %+v, want :443 nginx", ports[1])
	}
}

func TestCountSSEstablished(t *testing.T) {
	out := `State  Recv-Q Send-Q Local Address:Port Peer Address:Port
ESTAB  0      0      10.0.0.1:22        10.0.0.2:51000
LISTEN 0      128    0.0.0.0:22         0.0.0.0:*
ESTAB  0      0      10.0.0.1:443       10.0.0.9:51001
TIME-WAIT 0   0      10.0.0.1:80        10.0.0.3:40000
`
	if got := countSSEstablished(out); got != 2 {
		t.Errorf("countSSEstablished = %d, want 2", got)
	}
}

func TestLinuxVPNInterfaces(t *testing.T) {
	root := t.TempDir()
	// tailscale0 up, wg0 up (operstate unknown), eth0 (not a VPN), tun9 down.
	mk := func(name, state string) {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "operstate"), []byte(state+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("tailscale0", "unknown")
	mk("wg0", "up")
	mk("eth0", "up")
	mk("tun9", "down")

	got := linuxVPNInterfaces(root)
	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	if !set["tailscale0"] || !set["wg0"] {
		t.Errorf("got %v, want tailscale0 and wg0 active", got)
	}
	if set["eth0"] {
		t.Errorf("eth0 is not a VPN interface")
	}
	if set["tun9"] {
		t.Errorf("tun9 is down and should be excluded")
	}
}

func TestHostsDefaultsLinux(t *testing.T) {
	content := "127.0.0.1 localhost\n127.0.1.1 myhost\n::1 localhost ip6-localhost\nff02::1 ip6-allnodes\n10.1.2.3 corp.internal\n"
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := readHostsOverrides(path, hostsDefaultsLinux)
	if len(entries) != 1 || entries[0].IP != "10.1.2.3" {
		t.Errorf("entries = %+v, want only 10.1.2.3 (distro defaults filtered)", entries)
	}
}

func TestProxyFromEnv(t *testing.T) {
	t.Setenv("http_proxy", "http://proxy:3128")
	t.Setenv("HTTPS_PROXY", "http://sproxy:3129")
	t.Setenv("all_proxy", "socks5://sox:1080")
	pc := proxyFromEnv()
	if pc.HTTP != "http://proxy:3128" || pc.HTTPS != "http://sproxy:3129" || pc.SOCKS != "socks5://sox:1080" {
		t.Errorf("proxyFromEnv = %+v", pc)
	}
}

func TestCollectLinuxUsesSS(t *testing.T) {
	// With OS pinned to Linux, Collect should route to the Linux backend,
	// which queries ss (not lsof/route/scutil).
	defer hostos.SetForTest(hostos.Linux)()
	var sawSS bool
	stub := func(name string, args ...string) ([]byte, error) {
		if name == "ss" {
			sawSS = true
		}
		if name == "route" || name == "scutil" || name == "nettop" {
			t.Errorf("Linux backend invoked macOS tool %q", name)
		}
		return nil, nil
	}
	_ = Collect(stub)
	if !sawSS {
		t.Error("Linux Collect should query ss")
	}
}
