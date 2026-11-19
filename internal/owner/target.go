// Package owner answers "who has this?" for a port, a file path, or a pid:
// which processes listen on or hold connections from a local port, which
// hold a path open (fd, cwd, executable), and who launched them.
package owner

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Kind is the class of thing a Target names.
type Kind string

const (
	KindPort Kind = "port"
	KindPath Kind = "path"
	KindPID  Kind = "pid"
)

// Target is a parsed owner query.
type Target struct {
	Kind  Kind   `json:"kind"`
	Raw   string `json:"raw"`
	Proto string `json:"proto,omitempty"` // "tcp", "udp", or "" for both
	Port  int    `json:"port,omitempty"`
	Path  string `json:"path,omitempty"`
	PID   int    `json:"pid,omitempty"`
}

// ParseTarget classifies s. Relative paths are resolved against cwd.
func ParseTarget(s, cwd string) (Target, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Target{}, fmt.Errorf("empty target")
	}
	if t, ok, err := parsePortTarget(s); ok {
		return t, err
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n <= 0 {
			return Target{}, fmt.Errorf("invalid pid %q", s)
		}
		return Target{Kind: KindPID, Raw: s, PID: n}, nil
	}
	if strings.Contains(s, "/") || strings.HasPrefix(s, ".") {
		p := s
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		return Target{Kind: KindPath, Raw: s, Path: filepath.Clean(p)}, nil
	}
	return Target{}, fmt.Errorf("unrecognised target %q (want :PORT, tcp:PORT, udp:PORT, a path, or a pid)", s)
}

func parsePortTarget(s string) (Target, bool, error) {
	i := strings.Index(s, ":")
	if i < 0 || strings.Contains(s, "/") {
		return Target{}, false, nil
	}
	proto := strings.ToLower(s[:i])
	if proto != "" && proto != "tcp" && proto != "udp" {
		return Target{}, false, nil
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port < 1 || port > 65535 {
		return Target{}, true, fmt.Errorf("invalid port in %q (want 1-65535)", s)
	}
	return Target{Kind: KindPort, Raw: s, Proto: proto, Port: port}, true, nil
}
