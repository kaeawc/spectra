// Package lineage answers "who started this process, and what did it start?"
// from a process snapshot: the ancestor chain up to pid 1 and the descendant
// subtree with per-node and subtree resource totals.
package lineage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// Node flags.
const (
	FlagZombie     = "zombie"
	FlagReparented = "reparented"
)

// ErrNotFound reports that the requested pid is not in the snapshot.
var ErrNotFound = errors.New("no such process")

// Ancestor is one link of the chain, nearest-first starting at the target.
type Ancestor struct {
	PID       int       `json:"pid"`
	PPID      int       `json:"ppid"`
	User      string    `json:"user,omitempty"`
	StartTime time.Time `json:"start_time,omitempty"`
	Command   string    `json:"command"`
}

// Node is a process in the descendant tree with subtree totals.
type Node struct {
	PID           int       `json:"pid"`
	PPID          int       `json:"ppid"`
	User          string    `json:"user,omitempty"`
	StartTime     time.Time `json:"start_time,omitempty"`
	Command       string    `json:"command"`
	RSSKiB        int64     `json:"rss_kib"`
	CPUPct        float64   `json:"cpu_pct"`
	Flags         []string  `json:"flags,omitempty"`
	Count         int       `json:"count"`
	SubtreeRSSKiB int64     `json:"subtree_rss_kib"`
	SubtreeCPUPct float64   `json:"subtree_cpu_pct"`
	Children      []*Node   `json:"children,omitempty"`
}

// Result is the lineage of one process.
type Result struct {
	PID       int        `json:"pid"`
	Flags     []string   `json:"flags,omitempty"`
	Ancestors []Ancestor `json:"ancestors"`
	Tree      *Node      `json:"tree"`
}

// Collect snapshots the processes with c and builds pid's lineage. It is
// what `spectra lineage --json` prints and what the daemon's lineage.report
// returns, so both build it here.
func Collect(ctx context.Context, c process.Collector, opts process.CollectOptions, pid int) (*Result, error) {
	return Build(c.Collect(ctx, opts), pid)
}

// Build computes the lineage of pid from procs.
func Build(procs []process.Info, pid int) (*Result, error) {
	byPID := make(map[int]process.Info, len(procs))
	children := map[int][]int{}
	for _, pr := range procs {
		byPID[pr.PID] = pr
	}
	for _, pr := range procs {
		if pr.PID != pr.PPID {
			children[pr.PPID] = append(children[pr.PPID], pr.PID)
		}
	}
	target, ok := byPID[pid]
	if !ok {
		return nil, fmt.Errorf("pid %d: %w", pid, ErrNotFound)
	}
	for _, c := range children {
		sort.Ints(c)
	}
	seen := map[int]bool{}
	res := &Result{
		PID:       pid,
		Ancestors: chain(target, byPID),
		Tree:      buildNode(target, byPID, children, seen),
	}
	res.Flags = Reparented(target)
	return res, nil
}

func chain(target process.Info, byPID map[int]process.Info) []Ancestor {
	var out []Ancestor
	seen := map[int]bool{}
	cur := target
	for {
		seen[cur.PID] = true
		out = append(out, Ancestor{PID: cur.PID, PPID: cur.PPID, User: cur.User, StartTime: cur.StartTime, Command: cur.Command})
		if cur.PID == 1 || cur.PPID <= 0 || seen[cur.PPID] {
			return out
		}
		parent, ok := byPID[cur.PPID]
		if !ok {
			return out
		}
		cur = parent
	}
}

func buildNode(pr process.Info, byPID map[int]process.Info, children map[int][]int, seen map[int]bool) *Node {
	seen[pr.PID] = true
	n := &Node{
		PID: pr.PID, PPID: pr.PPID, User: pr.User, StartTime: pr.StartTime, Command: pr.Command,
		RSSKiB: pr.RSSKiB, CPUPct: pr.CPUPct,
		Count: 1, SubtreeRSSKiB: pr.RSSKiB, SubtreeCPUPct: pr.CPUPct,
	}
	if IsZombie(pr) {
		n.Flags = append(n.Flags, FlagZombie)
	}
	for _, cpid := range children[pr.PID] {
		if seen[cpid] {
			continue
		}
		c := buildNode(byPID[cpid], byPID, children, seen)
		n.Children = append(n.Children, c)
		n.Count += c.Count
		n.SubtreeRSSKiB += c.SubtreeRSSKiB
		n.SubtreeCPUPct += c.SubtreeCPUPct
	}
	return n
}

// IsZombie infers a zombie from the command form because the collector
// records no process state (the Linux stat state letter is not exposed).
// A procps-style "name <defunct>" whole command line with zero VSize is
// direct evidence; a live process merely mentioning "<defunct>" in its
// arguments is not. On Linux an empty cmdline yields FullCommandLine
// "[comm]"; with zero VSize that is a zombie or a kernel thread, and kernel
// threads are children of pid 0/2, so those parents are excluded (pid 1 may
// adopt orphaned zombies). macOS "(name)" is deliberately not used: ps prints
// it for live processes whose arguments it cannot read.
func IsZombie(pr process.Info) bool {
	if pr.VSizeKiB != 0 {
		return false
	}
	if isDefunct(pr.Command) || isDefunct(pr.FullCommandLine) {
		return true
	}
	return pr.Command != "" && pr.FullCommandLine == "["+pr.Command+"]" &&
		pr.PPID != 0 && pr.PPID != 2
}

// isDefunct matches procps "name <defunct>" and the bare "<defunct>" macOS ps
// prints for a zombie.
func isDefunct(s string) bool {
	if s == "<defunct>" {
		return true
	}
	name, ok := strings.CutSuffix(s, " <defunct>")
	return ok && name != "" && !strings.ContainsAny(name, " \t")
}

// Reparented returns the reparented flag for a non-root process whose parent
// is pid 1. It is a heuristic: per-user launchd agents also have PPID 1 and
// can false-positive.
func Reparented(pr process.Info) []string {
	if pr.PPID == 1 && pr.UID != 0 {
		return []string{FlagReparented}
	}
	return nil
}
