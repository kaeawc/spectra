// Package mallochistory summarizes `malloc_history` output — allocation
// backtraces recorded when a process runs under MallocStackLogging. It groups
// the heaviest allocation sites and extracts per-address backtraces so a leak
// can be attributed to the call stacks that made it. It reads only.
package mallochistory

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// AllocSite is one unique allocation stack and its total footprint, from
// `malloc_history -allBySize`.
type AllocSite struct {
	Bytes int64    `json:"bytes"`
	Calls int      `json:"calls"`
	Stack []string `json:"stack"`
}

// Backtrace is one recorded event (ALLOC or FREE) for a specific address, from
// `malloc_history <pid> <address>`.
type Backtrace struct {
	Kind   string   `json:"kind"`
	Frames []string `json:"frames"`
}

var (
	// allBySizeLine matches "16 calls for 262144 bytes: <stack>".
	allBySizeLine = regexp.MustCompile(`^\s*(\d+)\s+calls?\s+for\s+(\d+)\s+bytes:\s*(.*\S)\s*$`)
	// eventLine matches a per-address backtrace line beginning ALLOC/FREE.
	eventLine = regexp.MustCompile(`^\s*(ALLOC|FREE)\b.*?:\s*(.*\S)\s*$`)
)

// StackLoggingDisabled reports whether the output says MallocStackLogging was
// not enabled in the target — the precondition everyone trips over.
func StackLoggingDisabled(out string) bool {
	l := strings.ToLower(out)
	if !strings.Contains(l, "stack logging") {
		return false
	}
	return strings.Contains(l, "not enabled") ||
		strings.Contains(l, "was not enabled") ||
		strings.Contains(l, "not being recorded") ||
		strings.Contains(l, "no malloc") ||
		strings.Contains(l, "not recording")
}

// ParseAllBySize parses `malloc_history -allBySize` into allocation sites
// ranked by total bytes, keeping the top n (0 keeps all).
func ParseAllBySize(out string, n int) []AllocSite {
	var sites []AllocSite
	for _, line := range strings.Split(out, "\n") {
		m := allBySizeLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		calls, _ := strconv.Atoi(m[1])
		bytes, _ := strconv.ParseInt(m[2], 10, 64)
		sites = append(sites, AllocSite{Bytes: bytes, Calls: calls, Stack: splitStack(m[3])})
	}
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].Bytes != sites[j].Bytes {
			return sites[i].Bytes > sites[j].Bytes
		}
		return sites[i].Calls > sites[j].Calls
	})
	if n > 0 && len(sites) > n {
		sites = sites[:n]
	}
	return sites
}

// ParseAddress parses `malloc_history <pid> <address>` into its ALLOC/FREE
// backtraces.
func ParseAddress(out string) []Backtrace {
	var traces []Backtrace
	for _, line := range strings.Split(out, "\n") {
		m := eventLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		traces = append(traces, Backtrace{Kind: m[1], Frames: splitStack(m[2])})
	}
	return traces
}

// splitStack turns a "a | b | c" backtrace into frames, sanitizing each and
// dropping empties.
func splitStack(s string) []string {
	var frames []string
	for _, f := range strings.Split(s, "|") {
		if t := sanitize(strings.TrimSpace(f)); t != "" {
			frames = append(frames, t)
		}
	}
	return frames
}

// sanitize strips terminal control bytes from text taken from the tool output.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
}
