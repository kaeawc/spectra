// Package orphans finds processes left behind by deleted worktrees and
// finished jobs: reparented to pid 1 with a working directory under a root.
package orphans

import (
	"errors"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/process"
)

// Options configures Find.
type Options struct {
	Roots   []hostload.Root
	MinAge  time.Duration
	Now     time.Time
	SelfPID int
	// Stat returns the error from stat-ing a path; nil assumes every path
	// exists. Only fs.ErrNotExist counts as missing.
	Stat func(path string) error
}

// Process is one orphan candidate.
type Process struct {
	PID            int     `json:"pid"`
	User           string  `json:"user,omitempty"`
	AgeSeconds     int64   `json:"age_seconds"`
	RSSKiB         int64   `json:"rss_kib"`
	CPUPct         float64 `json:"cpu_pct"`
	Cwd            string  `json:"cwd"`
	CwdMissing     bool    `json:"cwd_missing"`
	CwdUnreadable  bool    `json:"cwd_unreadable"`
	Command        string  `json:"command"`
	CommandLine    string  `json:"command_line"`
	Descendants    int     `json:"descendants"`
	SubtreeRSSKiB  int64   `json:"subtree_rss_kib"`
	DaemonByDesign bool    `json:"daemon_by_design"`
}

// Group is the candidates of one worktree. RSSKiB sums the subtree RSS of the
// non-daemon candidates; candidates are never each other's descendants, so
// nothing is counted twice.
type Group struct {
	Root            string    `json:"root"`
	Worktree        string    `json:"worktree"`
	WorktreeRemoved bool      `json:"worktree_removed"`
	Processes       []Process `json:"processes"`
	RSSKiB          int64     `json:"rss_kib"`
}

// Result is the outcome of Find. Total counts non-daemon candidates only;
// DaemonTotal counts the probably-intentional ones.
type Result struct {
	Groups      []Group
	Total       int
	DaemonTotal int
	Skipped     int // reparented processes whose cwd could not be read
}

// PIDs lists every non-daemon candidate pid in report order.
func (r Result) PIDs() []int {
	var out []int
	for _, g := range r.Groups {
		for _, p := range g.Processes {
			if !p.DaemonByDesign {
				out = append(out, p.PID)
			}
		}
	}
	return out
}

type groupKey struct{ root, worktree string }

// Find returns the orphan candidates in procs. cwds maps pid to working
// directory; a missing entry counts as unreadable.
func Find(procs []process.Info, cwds map[int]string, opts Options) Result {
	excluded := selfAndAncestors(procs, opts.SelfPID)
	stat := opts.Stat
	if stat == nil {
		stat = func(string) error { return nil }
	}
	exists := func(p string) bool { return stat(p) == nil }
	var res Result
	groups := map[groupKey]*Group{}
	for _, p := range procs {
		if excluded[p.PID] || len(lineage.Reparented(p)) == 0 || tooYoung(p, opts) {
			continue
		}
		cwd := cwds[p.PID]
		if cwd == "" {
			res.Skipped++
			continue
		}
		cwd, deleted := strings.CutSuffix(cwd, deletedSuffix)
		root := hostload.MatchRoot(cwd, opts.Roots)
		if root == "" {
			continue
		}
		wt, removed := worktreeOf(cwd, exists)
		k := groupKey{root, wt}
		g := groups[k]
		if g == nil {
			g = &Group{Root: root, Worktree: wt, WorktreeRemoved: removed}
			groups[k] = g
		}
		row := newProcess(p, cwd, cwdState(cwd, deleted, stat), opts.Now)
		row.Descendants, row.SubtreeRSSKiB = subtree(procs, p.PID)
		g.Processes = append(g.Processes, row)
		if row.DaemonByDesign {
			res.DaemonTotal++
			continue
		}
		g.RSSKiB += row.SubtreeRSSKiB
		res.Total++
	}
	res.Groups = sortGroups(groups)
	return res
}

// deletedSuffix is what Linux appends to the cwd link of a removed directory.
const deletedSuffix = " (deleted)"

type cwdStatus struct{ missing, unreadable bool }

func cwdState(cwd string, deleted bool, stat func(string) error) cwdStatus {
	if deleted {
		return cwdStatus{missing: true}
	}
	err := stat(cwd)
	switch {
	case err == nil:
		return cwdStatus{}
	case errors.Is(err, fs.ErrNotExist):
		return cwdStatus{missing: true}
	}
	return cwdStatus{unreadable: true}
}

// worktreeOf returns the repo or worktree root for cwd. When an ancestor sits
// directly under a directory named "worktrees", has no .git of its own, and a
// .git exists further up, that ancestor is a removed worktree and is returned
// instead of the main repo.
func worktreeOf(cwd string, exists func(string) bool) (string, bool) {
	removed := ""
	for p := cwd; ; {
		if exists(path.Join(p, ".git")) {
			if removed != "" {
				return removed, true
			}
			return p, false
		}
		parent := path.Dir(p)
		if parent == p {
			return cwd, false
		}
		if removed == "" && path.Base(parent) == "worktrees" {
			removed = p
		}
		p = parent
	}
}

func selfAndAncestors(procs []process.Info, self int) map[int]bool {
	out := map[int]bool{self: true}
	if res, err := lineage.Build(procs, self); err == nil {
		for _, a := range res.Ancestors {
			out[a.PID] = true
		}
	}
	return out
}

// age is zero when the collector recorded neither a start time nor elapsed.
func age(p process.Info, now time.Time) time.Duration {
	switch {
	case !p.StartTime.IsZero():
		return now.Sub(p.StartTime)
	case !p.StartedAt.IsZero():
		return now.Sub(p.StartedAt)
	}
	return p.Elapsed
}

// An unknown age is never proof of youth, so it is not filtered.
func tooYoung(p process.Info, opts Options) bool {
	a := age(p, opts.Now)
	return a > 0 && a < opts.MinAge
}

func newProcess(p process.Info, cwd string, st cwdStatus, now time.Time) Process {
	cpu := p.CPUPct
	if cpu == 0 {
		cpu = p.CPUPercent
	}
	line := p.FullCommandLine
	if line == "" {
		line = p.Command
	}
	return Process{
		PID: p.PID, User: p.User, AgeSeconds: int64(age(p, now) / time.Second),
		RSSKiB: p.RSSKiB, CPUPct: cpu, Cwd: cwd, CwdMissing: st.missing, CwdUnreadable: st.unreadable,
		Command: p.Command, CommandLine: line, DaemonByDesign: daemonByDesign(p),
	}
}

var daemonNames = map[string]bool{
	"tmux": true, "screen": true, "adb": true, "emulator": true, "caffeinate": true,
	"ssh-agent": true, "gpg-agent": true, "dockerd": true, "containerd": true,
	"colima": true, "syncthing": true,
}

var daemonPrefixes = []string{"qemu-system-", "lima"}

// daemonByDesign reports tools that detach from their parent on purpose.
func daemonByDesign(p process.Info) bool {
	for _, n := range []string{p.Command, p.ExecutablePath} {
		if n != "" && daemonName(strings.ToLower(path.Base(strings.TrimSpace(n))), p.FullCommandLine) {
			return true
		}
	}
	return false
}

func daemonName(base, cmdline string) bool {
	if daemonNames[base] {
		return true
	}
	for _, pre := range daemonPrefixes {
		if strings.HasPrefix(base, pre) {
			return true
		}
	}
	return base == "java" && (strings.Contains(cmdline, "GradleDaemon") || strings.Contains(cmdline, "KotlinCompileDaemon"))
}

func subtree(procs []process.Info, pid int) (descendants int, rssKiB int64) {
	res, err := lineage.Build(procs, pid)
	if err != nil {
		return 0, 0
	}
	return res.Tree.Count - 1, res.Tree.SubtreeRSSKiB
}

func sortGroups(m map[groupKey]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		sort.Slice(g.Processes, func(i, j int) bool {
			a, b := g.Processes[i], g.Processes[j]
			if a.SubtreeRSSKiB != b.SubtreeRSSKiB {
				return a.SubtreeRSSKiB > b.SubtreeRSSKiB
			}
			return a.PID < b.PID
		})
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RSSKiB != out[j].RSSKiB {
			return out[i].RSSKiB > out[j].RSSKiB
		}
		if out[i].Root != out[j].Root {
			return out[i].Root < out[j].Root
		}
		return out[i].Worktree < out[j].Worktree
	})
	return out
}
