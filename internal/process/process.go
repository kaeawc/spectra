// Package process captures a point-in-time snapshot of all running processes
// on the local machine. It implements the ProcessInfo collector described in
// docs/design/system-inventory.md and docs/inspection/running-processes.md.
//
// Collection is a single fork of `ps`; per-app attribution is a string-prefix
// match (no extra forks). CPU% and thread counts land with the daemon's ring
// buffer.
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
	PID             int    `json:"pid"`
	PPID            int    `json:"ppid"`
	UID             int    `json:"uid"`
	User            string `json:"user,omitempty"`
	Command         string `json:"command"`         // short (comm) — just the exe name
	FullCommandLine string `json:"full_command_line"` // full argv[0...] string
	RSSKiB          int64  `json:"rss_kib"`
	VSizeKiB        int64  `json:"vsize_kib"`

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

	// CmdRunner overrides exec.Command for testing.
	CmdRunner func(name string, args ...string) ([]byte, error)
}

// CollectAll returns all running processes. Any parse errors for individual
// rows are silently skipped; a partial result is still useful.
func CollectAll(_ context.Context, opts CollectOptions) []Info {
	run := opts.CmdRunner
	if run == nil {
		run = defaultRunner
	}
	// Omit comm= (can contain spaces); derive a short name from command=.
	// Column order: pid ppid rss vsz uid user command...
	out, err := run("ps", "-axwwo", "pid=,ppid=,rss=,vsz=,uid=,user=,command=")
	if err != nil {
		return nil
	}
	procs := parsePS(string(out))
	if len(opts.BundlePaths) > 0 {
		attributeBundles(procs, opts.BundlePaths)
	}
	return procs
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

func parseRow(line string) Info {
	// Order: pid ppid rss vsz uid user command...
	// The first 6 fields have no spaces. Everything from field 7 onward is
	// the full argv string (may contain spaces).
	fields := strings.Fields(line)
	if len(fields) < 7 {
		return Info{}
	}
	pid, _ := strconv.Atoi(fields[0])
	ppid, _ := strconv.Atoi(fields[1])
	rss, _ := strconv.ParseInt(fields[2], 10, 64)
	vsz, _ := strconv.ParseInt(fields[3], 10, 64)
	uid, _ := strconv.Atoi(fields[4])
	full := strings.Join(fields[6:], " ")
	return Info{
		PID:             pid,
		PPID:            ppid,
		RSSKiB:          rss,
		VSizeKiB:        vsz,
		UID:             uid,
		User:            fields[5],
		Command:         shortName(full),
		FullCommandLine: full,
	}
}

// shortName extracts a human-readable command name from the full command line.
// For absolute paths, it returns the last path component minus any flags.
// "/Applications/Slack.app/Contents/MacOS/Slack --args" → "Slack"
func shortName(cmd string) string {
	exe := strings.Fields(cmd)[0] // strip flags
	if idx := strings.LastIndex(exe, "/"); idx >= 0 {
		return exe[idx+1:]
	}
	return exe
}

// attributeBundles sets AppPath for each process whose full command line
// starts with one of the bundle paths. O(N*M) but both N and M are small
// enough that a simple loop is fine.
func attributeBundles(procs []Info, bundles []string) {
	for i := range procs {
		cmd := procs[i].FullCommandLine
		for _, b := range bundles {
			if strings.HasPrefix(cmd, b) {
				procs[i].AppPath = b
				break
			}
		}
	}
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
