package hostload

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// UnknownDir is the bucket for processes whose cwd could not be read.
const UnknownDir = "unknown"

// idleCPUPct is the summed CPU of the leading group below which the host is
// considered to have no meaningful load to attribute.
const idleCPUPct = 10.0

// Cause kinds.
const (
	CauseIndexing = "indexing"
	CauseRoots    = "roots"
	CauseOther    = "other"
	CauseNone     = "none"
)

// FS is the filesystem seam used for repo detection.
type FS struct {
	Exists func(path string) bool
}

// Root is a worktree root as typed, plus its symlink-resolved form (lsof
// reports resolved cwds, e.g. /private/tmp for /tmp).
type Root struct {
	Path     string
	Resolved string
}

// Options configures Analyze.
type Options struct {
	TopN  int
	Roots []Root
	FS    FS
	Now   time.Time
	// BootWindow is how recent a device start counts as a boot; 0 means
	// DefaultBootWindow.
	BootWindow time.Duration
}

// ProcRow is one process in a top table.
type ProcRow struct {
	PID     int     `json:"pid"`
	Command string  `json:"command"`
	CPUPct  float64 `json:"cpu_pct"`
	RSSKiB  int64   `json:"rss_kib"`
	Cwd     string  `json:"cwd,omitempty"`
	Dir     string  `json:"dir"`
}

// FamilyRow is one process family.
type FamilyRow struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Members int     `json:"members"`
	CPUPct  float64 `json:"cpu_pct"`
	RSSKiB  int64   `json:"rss_kib"`
}

// DirUsage is CPU and RSS summed for one directory (repo root or bare cwd).
type DirUsage struct {
	Dir    string  `json:"dir"`
	Procs  int     `json:"procs"`
	CPUPct float64 `json:"cpu_pct"`
	RSSKiB int64   `json:"rss_kib"`
}

// Cause names what leads the CPU load.
type Cause struct {
	Kind     string  `json:"kind"`
	Detail   string  `json:"detail"`
	CPUPct   float64 `json:"cpu_pct"`
	Root     string  `json:"root,omitempty"`
	Worktree string  `json:"worktree,omitempty"`
	// Runner and Repo are set for kind ci-runner: the runner (GitHub
	// Actions, GitLab, Buildkite) and the job's repository directory.
	Runner string `json:"runner,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

// Analysis is the attribution of host load.
type Analysis struct {
	TopCPU   []ProcRow   `json:"top_cpu"`
	TopRSS   []ProcRow   `json:"top_rss"`
	Families []FamilyRow `json:"families"`
	Dirs     []DirUsage  `json:"dirs"`
	Unknown  DirUsage    `json:"unknown"`
	Cause    Cause       `json:"cause"`
	Devices  Devices     `json:"devices"`
}

// Analyze attributes load across procs. cwds maps pid to working directory;
// a missing or empty entry lands in the unknown bucket.
func Analyze(procs []process.Info, cwds map[int]string, opts Options) Analysis {
	n := opts.TopN
	if n < 1 {
		n = 5
	}
	exists := opts.FS.Exists
	if exists == nil {
		exists = func(string) bool { return false }
	}
	memo := map[string]string{}
	rollup := func(cwd string) string {
		if d, ok := memo[cwd]; ok {
			return d
		}
		d := RepoRoot(cwd, exists)
		memo[cwd] = d
		return d
	}

	rows := make([]ProcRow, 0, len(procs))
	dirs := map[string]*DirUsage{}
	a := Analysis{Unknown: DirUsage{Dir: UnknownDir}}
	for _, p := range procs {
		cpu := procCPU(p)
		row := ProcRow{PID: p.PID, Command: p.Command, CPUPct: cpu, RSSKiB: p.RSSKiB, Cwd: cwds[p.PID]}
		if row.Cwd == "" {
			row.Dir = UnknownDir
			a.Unknown.Procs++
			a.Unknown.CPUPct += cpu
			a.Unknown.RSSKiB += p.RSSKiB
		} else {
			row.Dir = rollup(row.Cwd)
			u := dirs[row.Dir]
			if u == nil {
				u = &DirUsage{Dir: row.Dir}
				dirs[row.Dir] = u
			}
			u.Procs++
			u.CPUPct += cpu
			u.RSSKiB += p.RSSKiB
		}
		rows = append(rows, row)
	}

	byCPU := sortedRows(rows, func(x, y ProcRow) bool { return x.CPUPct > y.CPUPct })
	byRSS := sortedRows(rows, func(x, y ProcRow) bool { return x.RSSKiB > y.RSSKiB })
	a.TopCPU = head(byCPU, n)
	a.TopRSS = head(byRSS, n)
	ci := ciOutsideRoots(attributeCI(procs, cwds), cwds, opts.Roots)
	a.Families = topFamilies(procs, opts.Now, n, ci)
	a.Dirs = topDirs(dirs, n)
	a.Cause = classify(rows, opts.Roots, ci)
	a.Devices = CollectDevices(procs, cwds, opts.Roots, opts.Now, opts.BootWindow)
	return a
}

func procCPU(p process.Info) float64 {
	if p.CPUPct != 0 {
		return p.CPUPct
	}
	return p.CPUPercent
}

func sortedRows(rows []ProcRow, less func(x, y ProcRow) bool) []ProcRow {
	out := append([]ProcRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool {
		if less(out[i], out[j]) {
			return true
		}
		if less(out[j], out[i]) {
			return false
		}
		return out[i].PID < out[j].PID
	})
	return out
}

func head[T any](s []T, n int) []T {
	if len(s) > n {
		s = s[:n]
	}
	return append([]T{}, s...)
}

// ciOutsideRoots drops CI attribution from processes whose cwd is under a
// --root, so a lane's own work is never reported as the runner's.
func ciOutsideRoots(ci map[int]ciWork, cwds map[int]string, roots []Root) map[int]ciWork {
	for pid := range ci {
		if MatchRoot(cwds[pid], roots) != "" {
			delete(ci, pid)
		}
	}
	return ci
}

// topFamilies groups CI runner work into one ci-runner family per runner
// and everything else through process.GroupFamilies.
func topFamilies(procs []process.Info, now time.Time, n int, ci map[int]ciWork) []FamilyRow {
	rest := make([]process.Info, 0, len(procs))
	runners := map[string]*FamilyRow{}
	for _, p := range procs {
		w, ok := ci[p.PID]
		if !ok {
			rest = append(rest, p)
			continue
		}
		f := runners[w.Runner]
		if f == nil {
			f = &FamilyRow{Name: ciFamilyName(w.Runner), Kind: FamilyCIRunner}
			runners[w.Runner] = f
		}
		f.Members++
		f.CPUPct += procCPU(p)
		f.RSSKiB += p.RSSKiB
	}
	out := make([]FamilyRow, 0, len(runners)+n)
	for _, f := range process.GroupFamilies(rest, now, 1) {
		out = append(out, FamilyRow{Name: f.Name, Kind: f.Kind, Members: f.Members, CPUPct: f.CPUPct, RSSKiB: f.RSSKiB})
	}
	labels := make([]string, 0, len(runners))
	for l := range runners {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	for _, l := range labels {
		out = append(out, *runners[l])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CPUPct != out[j].CPUPct {
			return out[i].CPUPct > out[j].CPUPct
		}
		return out[i].Kind == FamilyCIRunner && out[j].Kind != FamilyCIRunner
	})
	return head(out, n)
}

func topDirs(dirs map[string]*DirUsage, n int) []DirUsage {
	out := make([]DirUsage, 0, len(dirs))
	for _, u := range dirs {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CPUPct != out[j].CPUPct {
			return out[i].CPUPct > out[j].CPUPct
		}
		if out[i].RSSKiB != out[j].RSSKiB {
			return out[i].RSSKiB > out[j].RSSKiB
		}
		return out[i].Dir < out[j].Dir
	})
	return head(out, n)
}

// RepoRoot returns the nearest ancestor of cwd holding a .git entry (file or
// directory, so linked worktrees match), or cwd itself when there is none.
func RepoRoot(cwd string, exists func(string) bool) string {
	for p := cwd; ; {
		if exists(path.Join(p, ".git")) {
			return p
		}
		parent := path.Dir(p)
		if parent == p {
			return cwd
		}
		p = parent
	}
}

func isIndexer(cmd string) bool {
	c := strings.ToLower(path.Base(cmd))
	switch c {
	case "mds", "mds_stores", "fseventsd":
		return true
	}
	return strings.HasPrefix(c, "mdworker") || strings.HasPrefix(c, "tracker-miner") || strings.HasPrefix(c, "baloo")
}

func underRoot(cwd, root string) bool {
	return cwd == root || strings.HasPrefix(cwd, strings.TrimSuffix(root, "/")+"/")
}

type dirGroup struct {
	cpu  float64
	root string
	top  *ProcRow
}

type candidate struct {
	cpu   float64
	cause Cause
}

type groups struct {
	indexing float64
	indexers []string
	dirs     map[string]*dirGroup
	ci       map[string]*ciGroup
	singles  []*ProcRow
}

// classify sums CPU per candidate group over every collected row (all
// indexers together, each CI runner, each directory, each remaining
// single process) and names the largest. Ties prefer indexing, then
// directories under a --root, then CI runner work, then other directories,
// then single processes. CI attribution never covers a cwd under a --root,
// so a leading lane still wins over runner work.
func classify(rows []ProcRow, roots []Root, ci map[int]ciWork) Cause {
	g := groups{dirs: map[string]*dirGroup{}, ci: map[string]*ciGroup{}}
	for i := range rows {
		g.add(&rows[i], roots, ci)
	}
	lead := candidate{}
	for _, c := range g.candidates() {
		if c.cpu > lead.cpu {
			lead = c
		}
	}
	if lead.cause.Kind == CauseCIRunner {
		if c, ok := g.rootBeatsCI(lead); ok {
			lead = c
		}
	}
	if lead.cpu < idleCPUPct {
		return Cause{Kind: CauseNone, CPUPct: lead.cpu, Detail: fmt.Sprintf("host is idle: leading group uses under %.0f%% CPU", idleCPUPct)}
	}
	return lead.cause
}

// rootTotal is one --root's CPU summed over all its worktrees.
type rootTotal struct {
	cpu     float64
	dir     string
	heavy   float64
	members int
}

// rootBeatsCI compares each --root's total CPU (all its worktrees together)
// with the leading CI runner's total, because CI is summed across every runner
// install while a lane is otherwise scored per worktree. The busiest root wins
// when its total is at least the CI total.
func (g *groups) rootBeatsCI(ci candidate) (candidate, bool) {
	totals := map[string]*rootTotal{}
	var order []string
	for _, dir := range sortedKeys(g.dirs) {
		d := g.dirs[dir]
		if d.root == "" {
			continue
		}
		t := totals[d.root]
		if t == nil {
			t = &rootTotal{}
			totals[d.root] = t
			order = append(order, d.root)
		}
		t.cpu += d.cpu
		t.members++
		if t.dir == "" || d.cpu > t.heavy {
			t.dir, t.heavy = dir, d.cpu
		}
	}
	var best string
	for _, r := range order {
		if best == "" || totals[r].cpu > totals[best].cpu {
			best = r
		}
	}
	if best == "" || totals[best].cpu < ci.cpu {
		return candidate{}, false
	}
	t := totals[best]
	c := dirCause(t.dir, g.dirs[t.dir])
	c.CPUPct = t.cpu
	c.Detail = fmt.Sprintf("%d worktree(s) under %s total %.0f%% CPU, at least the CI runner (%s) total of %.0f%%; heaviest is %s",
		t.members, best, t.cpu, ci.cause.Runner, ci.cpu, path.Base(t.dir))
	return candidate{cpu: t.cpu, cause: c}, true
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (g *groups) add(r *ProcRow, roots []Root, ci map[int]ciWork) {
	w, isCI := ci[r.PID]
	switch {
	case isIndexer(r.Command):
		g.indexing += r.CPUPct
		g.indexers = append(g.indexers, path.Base(r.Command))
	case isCI:
		c := g.ci[w.Runner]
		if c == nil {
			c = &ciGroup{runner: w.Runner, repos: map[string]float64{}}
			g.ci[w.Runner] = c
		}
		c.add(r, w.Repo)
	case r.Dir == UnknownDir || r.Dir == "/":
		// A cwd of "/" says nothing about who is running; judge by process.
		g.singles = append(g.singles, r)
	default:
		d := g.dirs[r.Dir]
		if d == nil {
			d = &dirGroup{}
			g.dirs[r.Dir] = d
		}
		d.add(r, roots)
	}
}

func (g *groups) candidates() []candidate {
	out := []candidate{{cpu: g.indexing, cause: Cause{Kind: CauseIndexing, CPUPct: g.indexing,
		Detail: "indexing leads the CPU table (" + strings.Join(g.indexers, ", ") + ")"}}}
	names := make([]string, 0, len(g.dirs))
	for d := range g.dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, rooted := range []bool{true, false} {
		for _, dir := range names {
			if d := g.dirs[dir]; (d.root != "") == rooted {
				out = append(out, candidate{cpu: d.cpu, cause: dirCause(dir, d)})
			}
		}
		if rooted {
			out = append(out, g.ciCandidates()...)
		}
	}
	for _, r := range g.singles {
		out = append(out, candidate{cpu: r.CPUPct, cause: Cause{Kind: CauseOther, CPUPct: r.CPUPct,
			Detail: fmt.Sprintf("process %s (pid %d) leads the CPU table", r.Command, r.PID)}})
	}
	return out
}

func (g *groups) ciCandidates() []candidate {
	runners := make([]string, 0, len(g.ci))
	for r := range g.ci {
		runners = append(runners, r)
	}
	sort.Strings(runners)
	out := make([]candidate, 0, len(runners))
	for _, r := range runners {
		c := g.ci[r]
		out = append(out, candidate{cpu: c.cpu, cause: Cause{Kind: CauseCIRunner, CPUPct: c.cpu, Runner: c.runner, Repo: c.heaviestRepo(), Detail: ciCauseDetail(c)}})
	}
	return out
}

func (g *dirGroup) add(r *ProcRow, roots []Root) {
	g.cpu += r.CPUPct
	if g.root == "" {
		g.root = MatchRoot(r.Cwd, roots)
	}
	if g.top == nil || r.CPUPct > g.top.CPUPct {
		g.top = r
	}
}

func dirCause(dir string, g *dirGroup) Cause {
	if g.root != "" {
		return Cause{Kind: CauseRoots, CPUPct: g.cpu, Root: g.root, Worktree: path.Base(dir),
			Detail: "worktree " + path.Base(dir) + " under " + g.root + " leads the CPU table"}
	}
	return Cause{Kind: CauseOther, CPUPct: g.cpu,
		Detail: fmt.Sprintf("directory %s leads the CPU table; top process %s (pid %d)", dir, g.top.Command, g.top.PID)}
}

// MatchRoot returns the root as the user typed it when cwd is under either
// its typed or symlink-resolved form.
func MatchRoot(cwd string, roots []Root) string {
	if cwd == "" {
		return ""
	}
	for _, r := range roots {
		if underRoot(cwd, filepath.Clean(r.Path)) || (r.Resolved != "" && underRoot(cwd, filepath.Clean(r.Resolved))) {
			return r.Path
		}
	}
	return ""
}
