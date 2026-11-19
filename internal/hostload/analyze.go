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
}

// Analysis is the attribution of host load.
type Analysis struct {
	TopCPU   []ProcRow   `json:"top_cpu"`
	TopRSS   []ProcRow   `json:"top_rss"`
	Families []FamilyRow `json:"families"`
	Dirs     []DirUsage  `json:"dirs"`
	Unknown  DirUsage    `json:"unknown"`
	Cause    Cause       `json:"cause"`
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
		d := repoRoot(cwd, exists)
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
	a.Families = topFamilies(procs, opts.Now, n)
	a.Dirs = topDirs(dirs, n)
	a.Cause = classify(head(byCPU, max(n, 5)), opts.Roots)
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

func topFamilies(procs []process.Info, now time.Time, n int) []FamilyRow {
	fams := process.GroupFamilies(procs, now, 1)
	out := make([]FamilyRow, 0, n)
	for _, f := range fams {
		if len(out) == n {
			break
		}
		out = append(out, FamilyRow{Name: f.Name, Kind: f.Kind, Members: f.Members, CPUPct: f.CPUPct, RSSKiB: f.RSSKiB})
	}
	return out
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

// repoRoot returns the nearest ancestor of cwd holding a .git entry (file or
// directory, so linked worktrees match), or cwd itself when there is none.
func repoRoot(cwd string, exists func(string) bool) string {
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

// classify sums CPU per candidate group over the same window of rows (all
// indexers together, each directory, each remaining single process) and
// names the largest. Ties prefer indexing, then directories, then singles.
func classify(rows []ProcRow, roots []Root) Cause {
	var indexing float64
	var indexers []string
	dirs := map[string]*dirGroup{}
	var singles []*ProcRow
	for i := range rows {
		r := &rows[i]
		switch {
		case isIndexer(r.Command):
			indexing += r.CPUPct
			indexers = append(indexers, path.Base(r.Command))
		case r.Dir == UnknownDir || r.Dir == "/":
			// A cwd of "/" says nothing about who is running; judge by process.
			singles = append(singles, r)
		default:
			g := dirs[r.Dir]
			if g == nil {
				g = &dirGroup{}
				dirs[r.Dir] = g
			}
			g.add(r, roots)
		}
	}

	lead := 0.0
	c := Cause{}
	if indexing > lead {
		lead = indexing
		c = Cause{Kind: CauseIndexing, CPUPct: indexing, Detail: "indexing leads the CPU table (" + strings.Join(indexers, ", ") + ")"}
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, dir := range names {
		if g := dirs[dir]; g.cpu > lead {
			lead = g.cpu
			c = dirCause(dir, g)
		}
	}
	for _, r := range singles {
		if r.CPUPct > lead {
			lead = r.CPUPct
			c = Cause{Kind: CauseOther, CPUPct: r.CPUPct, Detail: fmt.Sprintf("process %s (pid %d) leads the CPU table", r.Command, r.PID)}
		}
	}
	if lead < idleCPUPct {
		return Cause{Kind: CauseNone, CPUPct: lead, Detail: fmt.Sprintf("host is idle: leading group uses under %.0f%% CPU", idleCPUPct)}
	}
	return c
}

func (g *dirGroup) add(r *ProcRow, roots []Root) {
	g.cpu += r.CPUPct
	if g.root == "" {
		g.root = matchRoot(r.Cwd, roots)
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

// matchRoot returns the root as the user typed it when cwd is under either
// its typed or symlink-resolved form.
func matchRoot(cwd string, roots []Root) string {
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
