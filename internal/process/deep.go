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
	fdCount       int
	listenPorts   []int
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
		recordLSOFLine(idx, results, line)
	}
	applyDeepResults(procs, idx, results)
}

func recordLSOFLine(idx map[int]int, results map[int]*deepResult, line string) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return
	}
	if _, ok := idx[pid]; !ok {
		return
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
	if len(fields) >= 9 && strings.EqualFold(fields[7], "TCP") {
		recordTCPName(r, strings.Join(fields[8:], " "))
	}
}

func recordTCPName(r *deepResult, name string) {
	if strings.Contains(name, "(LISTEN)") {
		if port := parseListenPort(name); port > 0 {
			r.listenPorts = append(r.listenPorts, port)
		}
		return
	}
	if remote := parseRemoteAddress(name); remote != "" {
		r.outboundConns = append(r.outboundConns, remote)
	}
}

func parseListenPort(name string) int {
	addr := strings.TrimSuffix(name, " (LISTEN)")
	colon := strings.LastIndex(addr, ":")
	if colon < 0 {
		return 0
	}
	port, err := strconv.Atoi(addr[colon+1:])
	if err != nil || port <= 0 {
		return 0
	}
	return port
}

func parseRemoteAddress(name string) string {
	idx := strings.Index(name, "->")
	if idx < 0 {
		return ""
	}
	remote := name[idx+2:]
	if sp := strings.Index(remote, " "); sp >= 0 {
		remote = remote[:sp]
	}
	return remote
}

func applyDeepResults(procs []Info, idx map[int]int, results map[int]*deepResult) {
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
