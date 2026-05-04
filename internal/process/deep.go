package process

import (
	"sort"
	"strconv"
	"strings"
)

// enrichDeep populates OpenFDs and ListeningPorts for each process via a
// single batched `lsof -F pcfn -p pid1,pid2,...` call.
// -F selects field output mode; p=pid, c=command, f=fd, n=name (address).
// Field output uses lines like "p412\0c0\0f29u\0...\0" but plain lsof
// output is easier to parse, so we use the regular output instead.
func enrichDeep(procs []Info, run func(string, ...string) ([]byte, error)) {
	// Build a comma-separated PID list.
	pids := make([]string, len(procs))
	for i, p := range procs {
		pids[i] = strconv.Itoa(p.PID)
	}
	pidArg := strings.Join(pids, ",")

	out, err := run("lsof", "-p", pidArg)
	if err != nil {
		return
	}
	parseLSOFDeep(procs, string(out))
}

// deepResult accumulates per-PID results during lsof parsing.
type deepResult struct {
	fdCount      int
	listenPorts  []int
	outboundConns []string
}

// parseLSOFDeep merges lsof output into the procs slice in-place.
//
// lsof output (non-field mode) columns:
//
//	COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
//
// FD values starting with a digit are open file descriptors.
// Rows with NODE=TCP and NAME containing "(LISTEN)" are listening ports.
func parseLSOFDeep(procs []Info, out string) {
	// Build a PID → index map for fast lookup.
	idx := make(map[int]int, len(procs))
	for i, p := range procs {
		idx[p.PID] = i
	}

	results := make(map[int]*deepResult, len(procs))

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		if _, ok := idx[pid]; !ok {
			continue
		}

		r := results[pid]
		if r == nil {
			r = &deepResult{}
			results[pid] = r
		}

		fd := fields[3]
		// Count rows where FD starts with a digit — those are real open descriptors.
		if len(fd) > 0 && fd[0] >= '0' && fd[0] <= '9' {
			r.fdCount++
		}

		// Listening port detection: NODE=TCP, NAME contains "(LISTEN)".
		// Outbound connection: NODE=TCP, NAME contains "->", no "(LISTEN)".
		if len(fields) >= 9 {
			node := strings.ToUpper(fields[7])
			if node == "TCP" {
				name := strings.Join(fields[8:], " ")
				if strings.Contains(name, "(LISTEN)") {
					// Extract port from "host:port (LISTEN)".
					addr := strings.TrimSuffix(name, " (LISTEN)")
					if colon := strings.LastIndex(addr, ":"); colon >= 0 {
						if port, err := strconv.Atoi(addr[colon+1:]); err == nil && port > 0 {
							r.listenPorts = append(r.listenPorts, port)
						}
					}
				} else if idx2 := strings.Index(name, "->"); idx2 >= 0 {
					// "local->remote (STATE)" — record the remote address.
					remote := name[idx2+2:]
					if sp := strings.Index(remote, " "); sp >= 0 {
						remote = remote[:sp]
					}
					if remote != "" {
						r.outboundConns = append(r.outboundConns, remote)
					}
				}
			}
		}
	}

	// Write results back into the procs slice.
	for pid, r := range results {
		i := idx[pid]
		procs[i].OpenFDs = r.fdCount
		if len(r.listenPorts) > 0 {
			sort.Ints(r.listenPorts)
			procs[i].ListeningPorts = r.listenPorts
		}
		if len(r.outboundConns) > 0 {
			sort.Strings(r.outboundConns)
			procs[i].OutboundConnections = r.outboundConns
		}
	}
}
