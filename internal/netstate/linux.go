package netstate

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// collectLinux gathers network state on Linux from /proc, /sys, and the
// iproute2 tools. Every source is best-effort; a missing file or absent
// tool leaves its field empty rather than failing the snapshot.
func collectLinux(run CmdRunner) State {
	var s State
	if data, err := os.ReadFile("/proc/net/route"); err == nil {
		s.DefaultRouteIface, s.DefaultRouteGW = parseProcNetRoute(data)
	}
	if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
		s.DNSServers = parseResolvConf(data)
	}
	s.Proxy = proxyFromEnv()
	s.HostsOverrides = readHostsOverrides("/etc/hosts", hostsDefaultsLinux)
	s.ListeningPorts = collectLinuxListeners(run)
	s.VPNInterfaces = linuxVPNInterfaces("/sys/class/net")
	s.VPNActive = len(s.VPNInterfaces) > 0
	if out, err := run("ss", "-tan"); err == nil {
		s.EstablishedConnectionsCount = countSSEstablished(string(out))
	}
	// Per-process throughput has no unprivileged, dependency-free source on
	// Linux (nettop is macOS-only), so it is left empty.
	return s
}

// parseProcNetRoute extracts the default route's interface and gateway from
// /proc/net/route. The default route is the row whose Destination is all
// zeros; Gateway is a little-endian hex IPv4 address.
func parseProcNetRoute(data []byte) (iface, gw string) {
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 { // header
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[1] != "00000000" { // Destination
			continue
		}
		return fields[0], hexLEToIPv4(fields[2])
	}
	return "", ""
}

// hexLEToIPv4 converts an 8-char little-endian hex string (as found in
// /proc/net/route, where addresses are stored in host/little-endian byte
// order) to dotted-decimal. The decoded bytes are the IP octets reversed.
// Returns "" on malformed input or the unspecified address.
func hexLEToIPv4(h string) string {
	b, err := hex.DecodeString(h)
	if err != nil || len(b) != 4 {
		return ""
	}
	if b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
		return ""
	}
	return strconv.Itoa(int(b[3])) + "." + strconv.Itoa(int(b[2])) + "." +
		strconv.Itoa(int(b[1])) + "." + strconv.Itoa(int(b[0]))
}

// parseResolvConf returns the unique nameserver addresses from
// resolv.conf content.
func parseResolvConf(data []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(line, "nameserver")
		if !ok {
			continue
		}
		ip := strings.TrimSpace(rest)
		if ip != "" && !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out
}

// proxyFromEnv reads the conventional Linux proxy environment variables.
// Linux has no system-wide proxy store; these are the de-facto standard.
func proxyFromEnv() ProxyConfig {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := os.Getenv(k); v != "" {
				return v
			}
		}
		return ""
	}
	return ProxyConfig{
		HTTP:  first("http_proxy", "HTTP_PROXY"),
		HTTPS: first("https_proxy", "HTTPS_PROXY"),
		SOCKS: first("all_proxy", "ALL_PROXY"),
	}
}

// hostsDefaultsLinux are the stock /etc/hosts IPs on common distributions,
// excluded from the "overrides" list.
var hostsDefaultsLinux = map[string]bool{
	"127.0.0.1": true,
	"127.0.1.1": true, // Debian/Ubuntu hostname mapping
	"::1":       true,
	"ff02::1":   true,
	"ff02::2":   true,
	"fe00::0":   true,
}

func collectLinuxListeners(run CmdRunner) []ListeningPort {
	var ports []ListeningPort
	if out, err := run("ss", "-tlnp"); err == nil {
		ports = append(ports, parseSSListen(string(out), "tcp")...)
	}
	if out, err := run("ss", "-ulnp"); err == nil {
		ports = append(ports, parseSSListen(string(out), "udp")...)
	}
	return ports
}

var ssProcessRe = regexp.MustCompile(`"([^"]+)",pid=(\d+)`)

// parseSSListen parses `ss -tlnp` / `ss -ulnp` output. Columns:
// State Recv-Q Send-Q Local-Address:Port Peer-Address:Port [Process].
func parseSSListen(out, proto string) []ListeningPort {
	var ports []ListeningPort
	for i, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "State") {
			continue // header
		}
		local, port, ok := splitHostPort(fields[3])
		if !ok {
			continue
		}
		lp := ListeningPort{Port: port, Proto: proto, LocalAddr: local}
		if len(fields) >= 6 {
			if m := ssProcessRe.FindStringSubmatch(strings.Join(fields[5:], " ")); m != nil {
				lp.Command = m[1]
				lp.PID, _ = strconv.Atoi(m[2])
			}
		}
		ports = append(ports, lp)
	}
	return ports
}

// splitHostPort splits an "addr:port" token (ss uses the last ':' as the
// port separator, so bracketed/implicit IPv6 hosts work).
func splitHostPort(s string) (host string, port int, ok bool) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 || idx == len(s)-1 {
		return "", 0, false
	}
	p, err := strconv.Atoi(s[idx+1:])
	if err != nil || p <= 0 {
		return "", 0, false
	}
	host = s[:idx]
	if host == "" || host == "*" {
		host = "*"
	}
	return host, p, true
}

// countSSEstablished counts ESTAB rows in `ss -tan` output.
func countSSEstablished(out string) int {
	count := 0
	for i, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if i == 0 && strings.EqualFold(fields[0], "State") {
			continue
		}
		if fields[0] == "ESTAB" {
			count++
		}
	}
	return count
}

// linuxVPNInterfaces scans sysClassNet (normally /sys/class/net) for tunnel
// interfaces that are up. Linux names VPN tunnels tun*/tap*/wg*/ppp* and
// Tailscale uses "tailscale0". tun devices frequently report operstate
// "unknown" while up, so anything not explicitly "down" counts.
func linuxVPNInterfaces(sysClassNet string) []string {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return nil
	}
	var active []string
	for _, e := range entries {
		name := e.Name()
		if !isVPNIfaceName(name) {
			continue
		}
		state := "unknown"
		if b, err := os.ReadFile(filepath.Join(sysClassNet, name, "operstate")); err == nil {
			state = strings.TrimSpace(string(b))
		}
		if state != "down" {
			active = append(active, name)
		}
	}
	return active
}

func isVPNIfaceName(name string) bool {
	for _, p := range []string{"tun", "tap", "wg", "ppp", "tailscale"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return name == "utun"
}
