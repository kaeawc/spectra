// Package orphans finds processes left behind by deleted worktrees and
// finished jobs: reparented to pid 1 with a working directory under a root.
package orphans

import (
	"sort"
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
	// FS.Exists reports whether a path exists; nil assumes every cwd exists.
	FS hostload.FS
}

// Process is one orphan candidate.
type Process struct {
	PID           int     `json:"pid"`
	User          string  `json:"user,omitempty"`
	AgeSeconds    int64   `json:"age_seconds"`
	RSSKiB        int64   `json:"rss_kib"`
	CPUPct        float64 `json:"cpu_pct"`
	Cwd           string  `json:"cwd"`
	CwdMissing    bool    `json:"cwd_missing"`
	Command       string  `json:"command"`
	Descendants   int     `json:"descendants"`
	SubtreeRSSKiB int64   `json:"subtree_rss_kib"`
}

// Group is the candidates of one worktree.
type Group struct {
	Root      string    `json:"root"`
	Worktree  string    `json:"worktree"`
	Processes []Process `json:"processes"`
	RSSKiB    int64     `json:"rss_kib"`
}

// Result is the outcome of Find.
type Result struct {
	Groups  []Group
	Total   int
	Skipped int // reparented processes whose cwd could not be read
}

// PIDs lists every candidate pid in report order.
func (r Result) PIDs() []int {
	var out []int
	for _, g := range r.Groups {
		for _, p := range g.Processes {
			out = append(out, p.PID)
		}
	}
	return out
}

// Find returns the orphan candidates in procs. cwds maps pid to working
// directory; a missing entry counts as unreadable.
func Find(procs []process.Info, cwds map[int]string, opts Options) Result {
	excluded := selfAndAncestors(procs, opts.SelfPID)
	exists := opts.FS.Exists
	if exists == nil {
		exists = func(string) bool { return true }
	}
	var res Result
	groups := map[[2]string]*Group{}
	for _, p := range procs {
		if excluded[p.PID] || len(lineage.Reparented(p)) == 0 || tooYoung(p, opts) {
			continue
		}
		cwd := cwds[p.PID]
		if cwd == "" {
			res.Skipped++
			continue
		}
		root := hostload.MatchRoot(cwd, opts.Roots)
		if root == "" {
			continue
		}
		wt := hostload.RepoRoot(cwd, exists)
		g := groups[[2]string{root, wt}]
		if g == nil {
			g = &Group{Root: root, Worktree: wt}
			groups[[2]string{root, wt}] = g
		}
		row := newProcess(p, cwd, !exists(cwd), opts.Now)
		row.Descendants, row.SubtreeRSSKiB = subtree(procs, p.PID)
		g.Processes = append(g.Processes, row)
		g.RSSKiB += p.RSSKiB
		res.Total++
	}
	res.Groups = sortGroups(groups)
	return res
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

func newProcess(p process.Info, cwd string, missing bool, now time.Time) Process {
	cpu := p.CPUPct
	if cpu == 0 {
		cpu = p.CPUPercent
	}
	return Process{
		PID: p.PID, User: p.User, AgeSeconds: int64(age(p, now) / time.Second),
		RSSKiB: p.RSSKiB, CPUPct: cpu, Cwd: cwd, CwdMissing: missing, Command: p.Command,
	}
}

func subtree(procs []process.Info, pid int) (descendants int, rssKiB int64) {
	res, err := lineage.Build(procs, pid)
	if err != nil {
		return 0, 0
	}
	return res.Tree.Count - 1, res.Tree.SubtreeRSSKiB
}

func sortGroups(m map[[2]string]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		sort.Slice(g.Processes, func(i, j int) bool {
			a, b := g.Processes[i], g.Processes[j]
			if a.RSSKiB != b.RSSKiB {
				return a.RSSKiB > b.RSSKiB
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
