package startup

import (
	"strconv"
	"strings"
)

// serviceState is one row of the services table in `launchctl print <domain>`.
type serviceState struct {
	PID      int
	LastExit string
}

// parsePrintDisabled reads `launchctl print-disabled <domain>`. Each override
// line is `"label" => state`; current macOS prints disabled/enabled and older
// releases true/false (true meaning disabled).
func parsePrintDisabled(out []byte) map[string]bool {
	got := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		label, state, ok := strings.Cut(strings.TrimSpace(line), "=>")
		if !ok {
			continue
		}
		label = strings.TrimSpace(label)
		if len(label) < 2 || label[0] != '"' || label[len(label)-1] != '"' {
			continue
		}
		label = label[1 : len(label)-1]
		switch strings.TrimSpace(state) {
		case "disabled", "true":
			got[label] = true
		case "enabled", "false":
			got[label] = false
		}
	}
	return got
}

// parseDomainServices reads the `services = { ... }` table of
// `launchctl print gui/<uid>` or `launchctl print system`: every loaded job
// in the domain as `pid last-exit label`, pid 0 when not running.
func parseDomainServices(out []byte) map[string]serviceState {
	got := map[string]serviceState{}
	in := false
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if !in {
			in = trimmed == "services = {"
			continue
		}
		if trimmed == "}" {
			break
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		st := serviceState{PID: pid}
		if fields[1] != "-" {
			st.LastExit = fields[1]
		}
		got[strings.Join(fields[2:], " ")] = st
	}
	return got
}
