// Package netcap contains capture plumbing shared by future helper-backed
// packet capture workflows.
package netcap

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDuration = 30 * time.Second
	MaxDuration     = time.Minute
	DefaultSnapLen  = 262144
)

// Options describes one bounded tcpdump capture.
type Options struct {
	Interface string
	Output    string
	Duration  time.Duration
	SnapLen   int
	Host      string
	Port      int
	Proto     string
}

// BuildTCPDumpArgs validates Options and returns argv for tcpdump. Duration is
// validated here but enforced by the caller's process context.
func BuildTCPDumpArgs(opts Options) ([]string, error) {
	if opts.Interface == "" {
		return nil, fmt.Errorf("capture interface is required")
	}
	if !validToken(opts.Interface) {
		return nil, fmt.Errorf("invalid capture interface %q", opts.Interface)
	}
	if opts.Output == "" {
		return nil, fmt.Errorf("capture output path is required")
	}
	duration := opts.Duration
	if duration == 0 {
		duration = DefaultDuration
	}
	if duration <= 0 || duration > MaxDuration {
		return nil, fmt.Errorf("capture duration must be >0 and <= %s", MaxDuration)
	}
	snapLen := opts.SnapLen
	if snapLen == 0 {
		snapLen = DefaultSnapLen
	}
	if snapLen < 96 || snapLen > DefaultSnapLen {
		return nil, fmt.Errorf("capture snap length must be between 96 and %d", DefaultSnapLen)
	}
	filter, err := bpfFilterArgs(opts)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-i", opts.Interface,
		"-n",
		"-s", strconv.Itoa(snapLen),
		"-w", opts.Output,
	}
	if len(filter) > 0 {
		args = append(args, filter...)
	}
	return args, nil
}

func bpfFilterArgs(opts Options) ([]string, error) {
	var parts []string
	if opts.Proto != "" {
		proto := strings.ToLower(opts.Proto)
		switch proto {
		case "tcp", "udp":
			parts = append(parts, proto)
		default:
			return nil, fmt.Errorf("invalid capture protocol %q", opts.Proto)
		}
	}
	if opts.Host != "" {
		if !validHost(opts.Host) {
			return nil, fmt.Errorf("invalid capture host %q", opts.Host)
		}
		parts = appendBPFAnd(parts, "host", opts.Host)
	}
	if opts.Port > 0 {
		if opts.Port > 65535 {
			return nil, fmt.Errorf("invalid capture port %d", opts.Port)
		}
		parts = appendBPFAnd(parts, "port", strconv.Itoa(opts.Port))
	}
	return parts, nil
}

func appendBPFAnd(parts []string, next ...string) []string {
	if len(parts) > 0 {
		parts = append(parts, "and")
	}
	return append(parts, next...)
}

func validToken(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func validHost(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return s != ""
}
