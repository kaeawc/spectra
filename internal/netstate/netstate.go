// Package netstate captures a point-in-time snapshot of the machine's
// network configuration: default route, DNS resolvers, proxy settings,
// /etc/hosts overrides, and current listening ports.
//
// All collection is user-privilege, read-only, no network I/O.
// See docs/design/system-inventory.md#networkstate.
package netstate

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
)

// State is the NetworkState slice of a Spectra snapshot.
type State struct {
	DefaultRouteIface string          `json:"default_route_iface,omitempty"`
	DefaultRouteGW    string          `json:"default_route_gw,omitempty"`
	DNSServers        []string        `json:"dns_servers,omitempty"`
	Proxy             ProxyConfig     `json:"proxy"`
	HostsOverrides    []HostsEntry    `json:"hosts_overrides,omitempty"`
	ListeningPorts    []ListeningPort `json:"listening_ports,omitempty"`

	// VPNActive is true when at least one tunnel interface (utun*) is UP with
	// an assigned address. macOS uses utun interfaces for all VPN types:
	// Tailscale, Cisco AnyConnect, WireGuard, OpenVPN, and built-in IKEv2.
	VPNActive     bool     `json:"vpn_active"`
	VPNInterfaces []string `json:"vpn_interfaces,omitempty"`
}

// ProxyConfig is the system HTTP/HTTPS/SOCKS proxy configuration.
type ProxyConfig struct {
	HTTP  string `json:"http,omitempty"`
	HTTPS string `json:"https,omitempty"`
	SOCKS string `json:"socks,omitempty"`
}

// HostsEntry is one non-default line from /etc/hosts.
type HostsEntry struct {
	IP      string   `json:"ip"`
	Names   []string `json:"names"`
}

// ListeningPort is one TCP/UDP socket currently bound on the machine.
type ListeningPort struct {
	Port    int    `json:"port"`
	Proto   string `json:"proto"` // "tcp", "udp"
	PID     int    `json:"pid,omitempty"`
	AppPath string `json:"app_path,omitempty"`
}

// CmdRunner abstracts shell-out for testability.
type CmdRunner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Collect gathers network state. Any sub-command failure is silently
// absorbed; partial results are still valid.
func Collect(run CmdRunner) State {
	var s State
	if out, err := run("route", "-n", "get", "default"); err == nil {
		s.DefaultRouteIface, s.DefaultRouteGW = parseRoute(string(out))
	}
	if out, err := run("scutil", "--dns"); err == nil {
		s.DNSServers = parseDNS(string(out))
	}
	if out, err := run("scutil", "--proxy"); err == nil {
		s.Proxy = parseProxy(string(out))
	}
	s.HostsOverrides = readHostsOverrides("/etc/hosts")
	if out, err := run("lsof", "-i", "-P", "-n", "-sTCP:LISTEN"); err == nil {
		s.ListeningPorts = parseLSOFListen(string(out))
	}
	if out, err := run("ifconfig"); err == nil {
		s.VPNInterfaces = parseVPNInterfaces(string(out))
		s.VPNActive = len(s.VPNInterfaces) > 0
	}
	return s
}

// parseRoute extracts interface and gateway from `route -n get default`.
//
// Example:
//
//	interface: en0
//	gateway: 192.168.1.1
func parseRoute(out string) (iface, gw string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "interface":
				iface = v
			case "gateway":
				gw = v
			}
		}
	}
	return
}

// parseDNS extracts unique nameserver addresses from `scutil --dns`.
//
// Example relevant lines: "nameserver[0] : 8.8.8.8"
func parseDNS(out string) []string {
	seen := map[string]bool{}
	var result []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "nameserver") {
			continue
		}
		_, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		ip := strings.TrimSpace(v)
		if ip != "" && !seen[ip] {
			seen[ip] = true
			result = append(result, ip)
		}
	}
	return result
}

// parseProxy extracts HTTP/HTTPS/SOCKS proxy settings from `scutil --proxy`.
//
// Example:
//
//	HTTPSEnable : 1
//	HTTPSProxy : proxy.example.com
//	HTTPSPort  : 8080
func parseProxy(out string) ProxyConfig {
	kv := scutilKV(out)
	var pc ProxyConfig
	if kv["HTTPEnable"] == "1" {
		pc.HTTP = kv["HTTPProxy"] + portSuffix(kv["HTTPPort"])
	}
	if kv["HTTPSEnable"] == "1" {
		pc.HTTPS = kv["HTTPSProxy"] + portSuffix(kv["HTTPSPort"])
	}
	if kv["SOCKSEnable"] == "1" {
		pc.SOCKS = kv["SOCKSProxy"] + portSuffix(kv["SOCKSPort"])
	}
	return pc
}

func portSuffix(port string) string {
	if port == "" || port == "0" {
		return ""
	}
	return ":" + port
}

// scutilKV parses "Key : Value" lines from scutil output into a map.
func scutilKV(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m
}

// readHostsOverrides reads /etc/hosts and returns non-default, non-comment
// lines. hostsPath is parameterised for testing.
func readHostsOverrides(hostsPath string) []HostsEntry {
	// Default macOS /etc/hosts entries — skip these.
	defaults := map[string]bool{
		"127.0.0.1":   true,
		"255.255.255.255": true,
		"::1":         true,
		"fe80::1%lo0": true,
	}

	f, err := os.Open(hostsPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []HostsEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip := fields[0]
		if defaults[ip] {
			continue
		}
		entries = append(entries, HostsEntry{IP: ip, Names: fields[1:]})
	}
	return entries
}

// parseLSOFListen parses `lsof -i -P -n -sTCP:LISTEN` for listening ports.
//
// Relevant line format:
//
//	com.apple.WebKit  412   alice  29u  IPv4  ...  TCP *:8080 (LISTEN)
func parseLSOFListen(out string) []ListeningPort {
	var ports []ListeningPort
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		// Need at least: command pid user fd type ... name(LISTEN)
		if len(fields) < 9 {
			continue
		}
		addrField := fields[8]
		if !strings.HasSuffix(addrField, "(LISTEN)") {
			// Some lsof versions put (LISTEN) in a separate column.
			found := false
			for _, f := range fields {
				if f == "(LISTEN)" {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		// Address is the field before (LISTEN): "TCP *:8080" or "*:8080"
		addr := strings.TrimSuffix(addrField, "(LISTEN)")
		addr = strings.TrimSpace(addr)
		if idx := strings.LastIndex(addr, ":"); idx >= 0 {
			portStr := addr[idx+1:]
			if seen[portStr] {
				continue
			}
			seen[portStr] = true
			port := parsePort(portStr)
			if port > 0 {
				ports = append(ports, ListeningPort{Port: port, Proto: "tcp"})
			}
		}
	}
	return ports
}

func parsePort(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// parseVPNInterfaces scans `ifconfig` output for active utun interfaces.
// macOS assigns a utun* name to every VPN tunnel (IKEv2, Tailscale,
// AnyConnect, WireGuard, OpenVPN). An interface counts as active when
// its block contains an "inet" address line, meaning the tunnel is
// established and has an assigned IP.
//
// Example block that matches:
//
//	utun3: flags=8051<UP,POINTOPOINT,RUNNING,MULTICAST> mtu 1280
//	        inet 100.64.0.1 --> 100.64.0.1 netmask 0xffffffff
func parseVPNInterfaces(out string) []string {
	var active []string
	var current string
	hasInet := false

	commit := func() {
		if current != "" && hasInet {
			active = append(active, current)
		}
		current = ""
		hasInet = false
	}

	for _, line := range strings.Split(out, "\n") {
		// Interface header: name followed by colon, at column 0.
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			commit()
			if idx := strings.Index(line, ":"); idx > 0 {
				name := line[:idx]
				if strings.HasPrefix(name, "utun") {
					current = name
				}
			}
			continue
		}
		// Continuation line inside the current interface block.
		if current != "" && strings.Contains(line, "inet ") {
			hasInet = true
		}
	}
	commit()
	return active
}
