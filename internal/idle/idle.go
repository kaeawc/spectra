// Package idle finds long-idle holders: processes under a directory root (or
// named by pid) that have been alive a while but, together with every
// descendant, used almost no CPU over a measured window. A build-slot or lock
// holder doing real work shows CPU or busy children; one that is stuck
// waiting shows neither. This is evidence only; nothing is ever signalled.
package idle

import (
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// BasisWindow means CPU figures are deltas over Sample.Window. Any other
// basis (a lifetime or decaying average) is not evidence of idleness now.
const BasisWindow = "window"

const (
	maxChain       = 64
	maxDescendants = 64
	// startTolerance absorbs the whole-second precision of start times.
	startTolerance = time.Second
	deletedSuffix  = " (deleted)"
)

// Root is a directory as typed plus its symlink-resolved form; kernels
// report resolved working directories.
type Root struct {
	Path     string
	Resolved string
}

// Options configures Find.
type Options struct {
	Roots     []Root
	PIDs      []int
	MinAge    time.Duration
	MaxCPUPct float64 // summed over the process and all its descendants
	Now       time.Time
	SelfPID   int // excluded with its ancestors and descendants
}

// Sample is a process table with CPU measured over a window.
type Sample struct {
	Procs []process.Info
	// CPU is percent of one CPU over Window by pid. A missing pid means its
	// CPU could not be measured, which never counts as idle.
	CPU    map[int]float64
	Window time.Duration
	Basis  string
}

// Link is one ancestor in a parent chain.
type Link struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// Wait is cheap evidence of what a process is blocked on.
type Wait struct {
	WChan     string   `json:"wchan,omitempty"`      // Linux kernel wait channel
	LockFiles []string `json:"lock_files,omitempty"` // open files named like locks
}

// Member is one descendant of an idle holder.
type Member struct {
	PID     int       `json:"pid"`
	PPID    int       `json:"ppid"`
	Command string    `json:"command"`
	CPUPct  float64   `json:"cpu_pct"`
	Started time.Time `json:"started,omitempty"`
	Wait    *Wait     `json:"wait,omitempty"`
}

// Holder is one idle process and its (equally idle) subtree.
type Holder struct {
	PID         int       `json:"pid"`
	PPID        int       `json:"ppid"`
	User        string    `json:"user,omitempty"`
	Command     string    `json:"command"`
	CommandLine string    `json:"command_line,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	Matched     string    `json:"matched"` // the root it is under, or "pid"
	Started     time.Time `json:"started"`
	AgeSeconds  int64     `json:"age_seconds"`
	CPUPct      float64   `json:"cpu_pct"`
	TreeCPUPct  float64   `json:"tree_cpu_pct"`
	Descendants []Member  `json:"descendants"`
	// DescendantsTotal is the full count when Descendants was capped.
	DescendantsTotal int    `json:"descendants_total"`
	Chain            []Link `json:"chain,omitempty"` // nearest parent first
	Wait             *Wait  `json:"wait,omitempty"`
}

// Result is the outcome of Find.
type Result struct {
	Holders []Holder `json:"holders"`
	// Counts of in-scope processes that were not reported, by reason.
	Checked    int `json:"checked"`
	Young      int `json:"skipped_young"`
	Busy       int `json:"skipped_busy"`
	Unmeasured int `json:"skipped_unmeasured"`
	// Nested counts idle processes folded into an idle ancestor's subtree.
	Nested      int   `json:"nested"`
	MissingPIDs []int `json:"missing_pids,omitempty"`
}

type verdict int

const (
	vIdle verdict = iota
	vYoung
	vBusy
	vUnmeasured
)

type table struct {
	byPID    map[int]process.Info
	children map[int][]int
}

func newTable(procs []process.Info) table {
	t := table{byPID: make(map[int]process.Info, len(procs)), children: map[int][]int{}}
	for _, p := range procs {
		t.byPID[p.PID] = p
	}
	for _, p := range procs {
		if p.PPID != p.PID {
			t.children[p.PPID] = append(t.children[p.PPID], p.PID)
		}
	}
	for _, c := range t.children {
		sort.Ints(c)
	}
	return t
}

// descendants returns every pid below pid, breadth first.
func (t table) descendants(pid int) []int {
	var out []int
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range t.children[cur] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}

func (t table) chain(ppid int) []Link {
	var out []Link
	seen := map[int]bool{}
	for pid := ppid; pid > 0 && !seen[pid] && len(out) < maxChain; {
		seen[pid] = true
		p, ok := t.byPID[pid]
		if !ok {
			break
		}
		out = append(out, Link{PID: pid, Command: p.Command})
		pid = p.PPID
	}
	return out
}

// selfFamily is Spectra, its ancestors (the shell waiting on it is idle by
// construction) and its descendants (the tools it runs).
func (t table) selfFamily(self int) map[int]bool {
	out := map[int]bool{}
	if self <= 0 {
		return out
	}
	out[self] = true
	for _, l := range t.chain(t.byPID[self].PPID) {
		out[l.PID] = true
	}
	for _, d := range t.descendants(self) {
		out[d] = true
	}
	return out
}

// Find reports the topmost idle processes in scope. Nothing is reported
// unless CPU was measured over a window.
func Find(s Sample, cwds map[int]string, opts Options) Result {
	res := Result{Holders: []Holder{}}
	t := newTable(s.Procs)
	res.MissingPIDs = missing(t, opts.PIDs)
	excluded := t.selfFamily(opts.SelfPID)
	wanted := map[int]bool{}
	for _, pid := range opts.PIDs {
		wanted[pid] = true
	}
	procs := append([]process.Info(nil), s.Procs...)
	sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	idle := map[int]bool{}
	var found []Holder
	for _, p := range procs {
		if excluded[p.PID] {
			continue
		}
		cwd := strings.TrimSuffix(cwds[p.PID], deletedSuffix)
		matched := scope(p.PID, cwd, wanted, opts.Roots)
		if matched == "" {
			continue
		}
		res.Checked++
		v, tree := judge(t, p, s, opts)
		switch v {
		case vYoung:
			res.Young++
		case vBusy:
			res.Busy++
		case vUnmeasured:
			res.Unmeasured++
		case vIdle:
			idle[p.PID] = true
			found = append(found, newHolder(t, p, s, cwd, matched, tree, opts.Now))
		}
	}
	for _, h := range found {
		if hasIdleAncestor(h, idle) {
			res.Nested++
			continue
		}
		res.Holders = append(res.Holders, h)
	}
	return res
}

func missing(t table, pids []int) []int {
	var out []int
	for _, pid := range pids {
		if _, ok := t.byPID[pid]; !ok {
			out = append(out, pid)
		}
	}
	return out
}

// scope names why p is in scope: an explicit pid, or the root its cwd is under.
func scope(pid int, cwd string, wanted map[int]bool, roots []Root) string {
	if wanted[pid] {
		return "pid"
	}
	if cwd == "" {
		return ""
	}
	for _, r := range roots {
		if under(cwd, r.Path) || (r.Resolved != "" && under(cwd, r.Resolved)) {
			return r.Path
		}
	}
	return ""
}

func under(p, root string) bool {
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return p == root || strings.HasPrefix(p, root+"/")
}

// judge decides whether p and its whole subtree are idle. A descendant that
// started during the window counts as activity: a wrapper that keeps
// spawning short tasks is working even if no single sample is busy.
func judge(t table, p process.Info, s Sample, opts Options) (verdict, []int) {
	if s.Basis != BasisWindow || s.Window <= 0 {
		return vUnmeasured, nil
	}
	if p.StartTime.IsZero() {
		return vUnmeasured, nil
	}
	if opts.Now.Sub(p.StartTime) < opts.MinAge {
		return vYoung, nil
	}
	tree := t.descendants(p.PID)
	windowStart := opts.Now.Add(-s.Window - startTolerance)
	total := 0.0
	for _, pid := range append([]int{p.PID}, tree...) {
		c, ok := s.CPU[pid]
		if !ok {
			return vUnmeasured, nil
		}
		if pid != p.PID && t.byPID[pid].StartTime.After(windowStart) {
			return vBusy, nil
		}
		total += c
	}
	if total >= opts.MaxCPUPct {
		return vBusy, nil
	}
	return vIdle, tree
}

func newHolder(t table, p process.Info, s Sample, cwd, matched string, tree []int, now time.Time) Holder {
	h := Holder{
		PID: p.PID, PPID: p.PPID, User: p.User, Command: p.Command, CommandLine: p.FullCommandLine,
		Cwd: cwd, Matched: matched, Started: p.StartTime, AgeSeconds: int64(now.Sub(p.StartTime) / time.Second),
		CPUPct: s.CPU[p.PID], TreeCPUPct: s.CPU[p.PID], Chain: t.chain(p.PPID),
		Descendants: []Member{}, DescendantsTotal: len(tree),
	}
	for _, pid := range tree {
		d := t.byPID[pid]
		h.TreeCPUPct += s.CPU[pid]
		if len(h.Descendants) < maxDescendants {
			h.Descendants = append(h.Descendants, Member{PID: pid, PPID: d.PPID, Command: d.Command, CPUPct: s.CPU[pid], Started: d.StartTime})
		}
	}
	return h
}

func hasIdleAncestor(h Holder, idle map[int]bool) bool {
	for _, l := range h.Chain {
		if idle[l.PID] {
			return true
		}
	}
	return false
}

// Pids lists every reported holder and listed descendant, for wait evidence.
func (r Result) Pids() []int {
	var out []int
	for _, h := range r.Holders {
		out = append(out, h.PID)
		for _, d := range h.Descendants {
			out = append(out, d.PID)
		}
	}
	return out
}

// AttachWaits fills Wait on holders and descendants from waits.
func (r *Result) AttachWaits(waits map[int]*Wait) {
	for i := range r.Holders {
		h := &r.Holders[i]
		h.Wait = waits[h.PID]
		for j := range h.Descendants {
			h.Descendants[j].Wait = waits[h.Descendants[j].PID]
		}
	}
}
