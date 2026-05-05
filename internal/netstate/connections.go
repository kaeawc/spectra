package netstate

import (
	"strconv"
	"strings"
)

// Connection is one active TCP or UDP socket from `lsof -i -P -n`.
type Connection struct {
	PID        int    `json:"pid,omitempty"`
	Command    string `json:"command,omitempty"`
	Proto      string `json:"proto"` // "tcp", "udp"
	LocalAddr  string `json:"local_addr"`
	RemoteAddr string `json:"remote_addr,omitempty"` // empty for unconnected UDP
	State      string `json:"state,omitempty"`       // "established", "close_wait", etc.
}

// CollectConnections returns all active (non-LISTEN) TCP and UDP sockets.
// LISTEN entries are omitted — those appear in State.ListeningPorts.
func CollectConnections(run CmdRunner) []Connection {
	out, err := run("lsof", "-i", "-P", "-n")
	if err == nil {
		return parseLSOFConnections(string(out))
	}
	out, err = run("netstat", "-an")
	if err != nil {
		return nil
	}
	return parseNetstatConnections(string(out))
}

// parseLSOFConnections extracts active connections from `lsof -i -P -n` output.
//
// lsof column order: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME [STATE]
//
//	NODE is "TCP" or "UDP"; NAME is "local->remote"; STATE is "(ESTABLISHED)" etc.
//
// Example lines:
//
//	Slack  412  alice  29u  IPv4  0xabc  0t0  TCP  192.168.1.1:55123->52.1.2.3:443 (ESTABLISHED)
//	chrome 789  alice  41u  IPv4  0xdef  0t0  UDP  192.168.1.1:54812->8.8.8.8:53
func parseLSOFConnections(out string) []Connection {
	var conns []Connection
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		// Find the NODE field (TCP/UDP) by scanning; it immediately precedes NAME.
		// Typically at fields[7], but scan to be robust across lsof versions.
		nodeIdx := -1
		for i, f := range fields {
			upper := strings.ToUpper(f)
			if upper == "TCP" || upper == "UDP" {
				nodeIdx = i
				break
			}
		}
		if nodeIdx < 0 || nodeIdx+1 >= len(fields) {
			continue
		}
		proto := strings.ToLower(fields[nodeIdx])
		name := fields[nodeIdx+1] // local or local->remote

		// State is the next field if present (e.g. "(ESTABLISHED)").
		state := ""
		if nodeIdx+2 < len(fields) {
			raw := fields[nodeIdx+2]
			if strings.HasPrefix(raw, "(") && strings.HasSuffix(raw, ")") {
				state = strings.ToLower(raw[1 : len(raw)-1])
			}
		}

		// Skip LISTEN entries — those already appear in State.ListeningPorts.
		if state == "listen" {
			continue
		}

		// Split "local->remote" on "->".
		var local, remote string
		if idx := strings.Index(name, "->"); idx >= 0 {
			local = name[:idx]
			remote = name[idx+2:]
		} else {
			local = name
		}

		pid, _ := strconv.Atoi(fields[1])
		conns = append(conns, Connection{
			PID:        pid,
			Command:    fields[0],
			Proto:      proto,
			LocalAddr:  local,
			RemoteAddr: remote,
			State:      state,
		})
	}
	return conns
}

// parseNetstatConnections extracts system-wide active sockets from
// `netstat -an` output. netstat has no PID/command columns, so callers receive
// socket state only.
func parseNetstatConnections(out string) []Connection {
	var conns []Connection
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		proto := strings.ToLower(fields[0])
		if !strings.HasPrefix(proto, "tcp") && !strings.HasPrefix(proto, "udp") {
			continue
		}
		state := ""
		if strings.HasPrefix(proto, "tcp") && len(fields) >= 6 {
			state = strings.ToLower(fields[5])
		}
		if state == "listen" {
			continue
		}
		conns = append(conns, Connection{
			Proto:      trimProtoFamily(proto),
			LocalAddr:  normalizeNetstatAddr(fields[3]),
			RemoteAddr: netstatRemoteAddr(fields),
			State:      state,
		})
	}
	return conns
}

func trimProtoFamily(proto string) string {
	switch {
	case strings.HasPrefix(proto, "tcp"):
		return "tcp"
	case strings.HasPrefix(proto, "udp"):
		return "udp"
	default:
		return proto
	}
}

func netstatRemoteAddr(fields []string) string {
	if len(fields) < 5 {
		return ""
	}
	return normalizeNetstatAddr(fields[4])
}

func normalizeNetstatAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || addr == "*.*" {
		return ""
	}
	idx := strings.LastIndex(addr, ".")
	if idx < 0 {
		return addr
	}
	host, port := addr[:idx], addr[idx+1:]
	if port == "*" {
		return host
	}
	if _, err := strconv.Atoi(port); err != nil {
		return addr
	}
	return host + ":" + port
}
