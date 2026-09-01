// Package lsmp summarizes a process's Mach port table from `lsmp -p` output.
// A Mach port leak — a service accumulating send rights until it hits the
// per-task limit and wedges — is invisible in the raw per-port table; this
// counts port rights by type so a leak is obvious. It reads only.
package lsmp

import (
	"fmt"
	"regexp"
	"strings"
)

// portLeakThreshold is the total port count above which a leak note is added.
// The per-task limit is large; a healthy process rarely holds this many.
const portLeakThreshold = 10000

// Summary is the parsed Mach port picture of a process.
type Summary struct {
	TotalPorts     int      `json:"total_ports"`
	RecvRights     int      `json:"recv_rights"`
	SendRights     int      `json:"send_rights"`
	SendOnceRights int      `json:"send_once_rights"`
	PortSets       int      `json:"port_sets"`
	Notes          []string `json:"notes,omitempty"`
}

// portNameToken matches the leading hexadecimal port name that begins each
// data row of an lsmp report.
var portNameToken = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)

// Parse turns `lsmp -p` output into a rights summary. It keys on the leading
// hex port name and the documented rights keyword, so it tolerates the
// column-spacing differences between macOS versions.
func Parse(out string) Summary {
	var s Summary
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// A data row starts with a hex port name (or "-" for a port-set member).
		if !portNameToken.MatchString(fields[0]) && fields[0] != "-" {
			continue
		}
		s.TotalPorts++
		switch rightsOf(fields) {
		case "port-set":
			s.PortSets++
		case "send-once":
			s.SendOnceRights++
		case "send":
			s.SendRights++
		case "recv":
			s.RecvRights++
		}
	}
	if s.TotalPorts > portLeakThreshold {
		s.Notes = append(s.Notes, fmt.Sprintf("high Mach port count (%d) — investigate for a port-right leak", s.TotalPorts))
	}
	return s
}

// rightsOf returns the documented rights keyword present in a row's fields, or
// "" if none is found. "send-once" is checked before "send".
func rightsOf(fields []string) string {
	for _, f := range fields {
		switch f {
		case "port-set", "send-once", "send", "recv":
			return f
		}
	}
	return ""
}
