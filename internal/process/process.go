// Package process captures a point-in-time snapshot of all running processes
// on the local machine. It implements the ProcessInfo collector described in
// docs/design/system-inventory.md and docs/inspection/running-processes.md.
//
// Collection is a single fork of `ps`; per-app attribution is a string-prefix
// match (no extra forks). CPU% is captured from ps; thread counts are filled
// from proc_pidinfo on Darwin when available.
package process

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Info is one running process at snapshot time.
type Info struct {
	PID             int       `json:"pid"`
	PPID            int       `json:"ppid"`
	UID             int       `json:"uid"`
	User            string    `json:"user,omitempty"`
	Command         string    `json:"command"`            // short (comm) — just the exe name
	BSDName         string    `json:"bsd_name,omitempty"` // p_comm from libproc when available
	ExecutablePath  string    `json:"executable_path,omitempty"`
	FullCommandLine string    `json:"full_command_line"` // full argv[0...] string
	RSSKiB          int64     `json:"rss_kib"`
	VSizeKiB        int64     `json:"vsize_kib"`
	ThreadCount     int       `json:"thread_count"`         // number of process threads
	CPUPct          float64   `json:"cpu_pct"`              // CPU % at sample time (pcpu)
	StartTime       time.Time `json:"start_time,omitempty"` // process start time (lstart)

	// Deep fields — populated only when CollectOptions.Deep is true.
	OpenFDs             int      `json:"open_fds,omitempty"`             // open file descriptor count
	ListeningPorts      []int    `json:"listening_ports,omitempty"`      // TCP ports this process listens on
	OutboundConnections []string `json:"outbound_connections,omitempty"` // active TCP remote addresses (host:port)

	// AppPath is set when the process's executable path starts with a known
	// .app bundle path. Populated only when CollectAll is called with a set
	// of app paths (BundlePaths option).
	AppPath string `json:"app_path,omitempty"`
}

// CollectOptions parameterises CollectAll.
type CollectOptions struct {
	// BundlePaths, when non-empty, triggers bundle attribution: each process
	// whose executable lives inside one of these .app paths gets AppPath set.
	BundlePaths []string

	// Deep, when true, enriches each process with OpenFDs, ListeningPorts,
	// and OutboundConnections via a single batched lsof call. Adds ~50-100ms
	// per collection.
	Deep bool

	// CmdRunner overrides exec.Command for testing.
	CmdRunner func(name string, args ...string) ([]byte, error)

	// DetailCollector overrides the platform process-detail collector for
	// testing. On Darwin this is backed by libproc/proc_pidinfo.
	DetailCollector func([]Info) map[int]Details

	// ThreadCounter overrides the platform thread-count collector for testing.
	// Deprecated: use DetailCollector for new tests.
	ThreadCounter func([]Info) map[int]int
}

// Details contains direct per-process metadata collected without spawning
// extra tools where the platform exposes it.
type Details struct {
	ThreadCount    int
	BSDName        string
	ExecutablePath string
}

// CollectAll returns all running processes. Any parse errors for individual
// rows are silently skipped; a partial result is still useful.
func CollectAll(_ context.Context, opts CollectOptions) []Info {
	run := opts.CmdRunner
	if run == nil {
		run = defaultRunner
	}
	// Column order: pid ppid pcpu rss vsz uid user lstart command...
	// lstart produces a 5-token date "Dow Mon DD HH:MM:SS YYYY".
	// macOS ps does not support nlwp; ThreadCount is populated below by the
	// platform collector when available.
	out, err := run("ps", "-axwwo", "pid=,ppid=,pcpu=,rss=,vsz=,uid=,user=,lstart=,command=")
	if err != nil {
		return nil
	}
	procs := parsePS(string(out))
	applyProcessDetails(procs, opts.DetailCollector)
	threadCounter := opts.ThreadCounter
	if threadCounter == nil && opts.DetailCollector == nil {
		threadCounter = collectThreadCounts
	}
	applyThreadCounts(procs, threadCounter)
	if len(opts.BundlePaths) > 0 {
		attributeBundles(procs, opts.BundlePaths)
	}
	if opts.Deep && len(procs) > 0 {
		enrichDeep(procs, run)
	}
	return procs
}

func applyProcessDetails(procs []Info, collector func([]Info) map[int]Details) {
	if len(procs) == 0 {
		return
	}
	if collector == nil {
		collector = collectProcessDetails
	}
	details := collector(procs)
	for i := range procs {
		d := details[procs[i].PID]
		if d.ThreadCount > 0 {
			procs[i].ThreadCount = d.ThreadCount
		}
		if d.BSDName != "" {
			procs[i].BSDName = d.BSDName
		}
		if d.ExecutablePath != "" {
			procs[i].ExecutablePath = d.ExecutablePath
		}
	}
}

func applyThreadCounts(procs []Info, counter func([]Info) map[int]int) {
	if len(procs) == 0 {
		return
	}
	if counter == nil {
		return
	}
	counts := counter(procs)
	for i := range procs {
		if count := counts[procs[i].PID]; count > 0 {
			procs[i].ThreadCount = count
		}
	}
}

// defaultRunner runs the real ps command.
func defaultRunner(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// parsePS converts raw ps output to a slice of Info.
// Format: pid ppid rss vsz uid user comm command...
func parsePS(raw string) []Info {
	var out []Info
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p := parseRow(line)
		if p.PID > 0 {
			out = append(out, p)
		}
	}
	return out
}

// lstartLayout matches the macOS lstart format: "Mon Jan  2 15:04:05 2006"
// (single-digit days are space-padded in the raw string but normalised after
// Fields() splits on whitespace, so we use two-digit zero-padded day here).
const lstartLayout = "Mon Jan 2 15:04:05 2006"

func parseRow(line string) Info {
	// Order: pid ppid pcpu rss vsz uid user <lstart:5 tokens> command...
	// lstart is "Dow Mon DD HH:MM:SS YYYY" — always 5 space-separated tokens.
	// Minimum field count: 7 fixed + 5 lstart + 1 command = 13.
	fields := strings.Fields(line)
	if len(fields) < 13 {
		return Info{}
	}
	pid, _ := strconv.Atoi(fields[0])
	ppid, _ := strconv.Atoi(fields[1])
	cpu, _ := strconv.ParseFloat(fields[2], 64)
	rss, _ := strconv.ParseInt(fields[3], 10, 64)
	vsz, _ := strconv.ParseInt(fields[4], 10, 64)
	uid, _ := strconv.Atoi(fields[5])
	// fields[6] = user; fields[7:12] = lstart (5 tokens); fields[12:] = command
	lstartStr := strings.Join(fields[7:12], " ")
	var startTime time.Time
	if t, err := time.ParseInLocation(lstartLayout, lstartStr, time.Local); err == nil {
		startTime = t
	}
	full := strings.Join(fields[12:], " ")
	return Info{
		PID:             pid,
		PPID:            ppid,
		CPUPct:          cpu,
		RSSKiB:          rss,
		VSizeKiB:        vsz,
		UID:             uid,
		User:            fields[6],
		Command:         shortName(full),
		FullCommandLine: full,
		StartTime:       startTime,
	}
}

// shortName extracts a human-readable command name from the full command line.
// For absolute paths, it returns the last path component minus any flags.
// "/Applications/Slack.app/Contents/MacOS/Slack --args" → "Slack"
func shortName(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	exe := fields[0] // strip flags
	if idx := strings.LastIndex(exe, "/"); idx >= 0 {
		return exe[idx+1:]
	}
	return exe
}

// attributeBundles sets AppPath for each process whose full command line
// or executable path starts with one of the bundle paths. O(N*M) but both N
// and M are small enough that a simple loop is fine.
func attributeBundles(procs []Info, bundles []string) {
	for i := range procs {
		for _, b := range bundles {
			if processBelongsToBundle(procs[i], b) {
				procs[i].AppPath = b
				break
			}
		}
	}
}

func processBelongsToBundle(p Info, bundle string) bool {
	if pathInsideBundle(p.ExecutablePath, bundle) {
		return true
	}
	return strings.HasPrefix(p.FullCommandLine, bundle)
}

func pathInsideBundle(path, bundle string) bool {
	if path == "" || bundle == "" {
		return false
	}
	return path == bundle || strings.HasPrefix(path, strings.TrimRight(bundle, "/")+"/")
}

// GroupByApp returns processes grouped by AppPath. Processes with no
// AppPath are stored under the empty-string key.
func GroupByApp(procs []Info) map[string][]Info {
	m := make(map[string][]Info)
	for _, p := range procs {
		m[p.AppPath] = append(m[p.AppPath], p)
	}
	return m
}

// TotalRSS returns the sum of RSSKiB across all procs.
func TotalRSS(procs []Info) int64 {
	var total int64
	for _, p := range procs {
		total += p.RSSKiB
	}
	return total
}

// TreeNode represents one process in the parent-child hierarchy.
type TreeNode struct {
	Info     Info        `json:"info"`
	Children []*TreeNode `json:"children,omitempty"`
}

// BuildTree constructs a parent-child tree from a flat process list.
// Processes whose parent PID is unknown (not in the list) or self-referential
// become roots.
func BuildTree(procs []Info) []*TreeNode {
	nodes := make(map[int]*TreeNode, len(procs))
	for i := range procs {
		nodes[procs[i].PID] = &TreeNode{Info: procs[i]}
	}
	var roots []*TreeNode
	for _, p := range procs {
		node := nodes[p.PID]
		if parent, ok := nodes[p.PPID]; ok && p.PPID != p.PID {
			parent.Children = append(parent.Children, node)
		} else {
			roots = append(roots, node)
		}
	}
	return roots
}
