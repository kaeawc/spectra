package hostload

import (
	"fmt"
	"path"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
)

// CauseCIRunner is the cause kind for work run by a self-hosted CI runner on
// this host; FamilyCIRunner is the matching process family kind.
const (
	CauseCIRunner  = "ci-runner"
	FamilyCIRunner = "ci-runner"
)

// Runner labels.
const (
	RunnerGitHubActions = "GitHub Actions"
	RunnerGitLab        = "GitLab"
	RunnerBuildkite     = "Buildkite"
)

const maxAncestorHops = 64

// runnerBinaries maps a runner's lowercased process name to its label. Its
// descendants are that runner's work.
var runnerBinaries = map[string]string{
	"runner.worker":   RunnerGitHubActions,
	"runner.listener": RunnerGitHubActions,
	"gitlab-runner":   RunnerGitLab,
	"buildkite-agent": RunnerBuildkite,
}

// ciWork is a process attributed to a CI runner. Repo is the job's
// repository directory when a cwd names it, else empty.
type ciWork struct {
	Runner string
	Repo   string
}

func runnerLabel(p process.Info) string {
	if l := runnerBinaries[strings.ToLower(path.Base(p.Command))]; l != "" {
		return l
	}
	if p.ExecutablePath != "" {
		return runnerBinaries[strings.ToLower(path.Base(p.ExecutablePath))]
	}
	return ""
}

// GitHubWorkRepo returns the repository directory for a cwd under a
// self-hosted GitHub Actions work tree (<dir starting actions-runner>/_work/).
// The checkout is _work/<repo>/<repo>; a cwd in the outer directory or a
// runner-internal one (_work/_temp, _work/_actions) yields the outer
// directory or "" respectively. ok reports whether cwd is under _work at all.
func GitHubWorkRepo(cwd string) (repo string, ok bool) {
	parts := strings.Split(cwd, "/")
	for i := 1; i+1 < len(parts); i++ {
		if parts[i+1] != "_work" || !strings.HasPrefix(strings.ToLower(parts[i]), "actions-runner") {
			continue
		}
		rest := parts[i+2:]
		if len(rest) == 0 || rest[0] == "" || strings.HasPrefix(rest[0], "_") {
			return "", true
		}
		end := i + 3
		if len(rest) > 1 && rest[1] == rest[0] {
			end++
		}
		return strings.Join(parts[:end], "/"), true
	}
	return "", false
}

// directCI attributes p from its own name or cwd.
func directCI(p process.Info, cwd string) (ciWork, bool) {
	repo, underWork := GitHubWorkRepo(cwd)
	if l := runnerLabel(p); l != "" {
		return ciWork{Runner: l, Repo: repo}, true
	}
	if underWork {
		return ciWork{Runner: RunnerGitHubActions, Repo: repo}, true
	}
	return ciWork{}, false
}

// attributeCI maps pid to CI work for every process that is a runner, runs
// under a runner work tree, or descends from either.
func attributeCI(procs []process.Info, cwds map[int]string) map[int]ciWork {
	byPID := make(map[int]process.Info, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	out := map[int]ciWork{}
	for _, p := range procs {
		if w, ok := ciFor(p, byPID, cwds); ok {
			out[p.PID] = w
		}
	}
	return out
}

// ciFor walks p and its ancestors, nearest first. The runner label comes
// from the nearest attributable process; the repo from the nearest cwd under
// a work tree, so a job step's checkout wins over the runner's own cwd.
func ciFor(p process.Info, byPID map[int]process.Info, cwds map[int]string) (ciWork, bool) {
	var found ciWork
	ok := false
	for _, cur := range append([]process.Info{p}, ancestorsOf(p, byPID)...) {
		w, hit := directCI(cur, cwds[cur.PID])
		if !hit {
			continue
		}
		if !ok {
			found, ok = w, true
		}
		if found.Repo == "" {
			found.Repo = w.Repo
		}
		if found.Repo != "" {
			break
		}
	}
	return found, ok
}

// ancestorsOf returns p's parent chain, nearest first. It stops at a missing
// parent, a cycle, or a parent that started after its child (PID reuse).
func ancestorsOf(p process.Info, byPID map[int]process.Info) []process.Info {
	var out []process.Info
	seen := map[int]bool{p.PID: true}
	cur := p
	for len(out) < maxAncestorHops {
		parent, ok := byPID[cur.PPID]
		if !ok || seen[parent.PID] {
			break
		}
		if ps, cs := startOf(parent), startOf(cur); !ps.IsZero() && !cs.IsZero() && ps.After(cs) {
			break
		}
		seen[parent.PID] = true
		out = append(out, parent)
		cur = parent
	}
	return out
}

func ciFamilyName(runner string) string { return runner + " runner" }

// ciGroup is all work of one runner kind. Several runner installs often
// build the same repository at once, and only their sum says how much of
// the host CI is using, so jobs are summed rather than ranked apart.
type ciGroup struct {
	runner string
	cpu    float64
	repos  map[string]float64
	top    *ProcRow
}

func (g *ciGroup) add(r *ProcRow, repo string) {
	g.cpu += r.CPUPct
	if repo != "" {
		g.repos[repo] += r.CPUPct
	}
	if g.top == nil || r.CPUPct > g.top.CPUPct {
		g.top = r
	}
}

// heaviestRepo is the job directory using the most CPU, or "".
func (g *ciGroup) heaviestRepo() string {
	best := ""
	for repo, cpu := range g.repos {
		if best == "" || cpu > g.repos[best] || (cpu == g.repos[best] && repo < best) {
			best = repo
		}
	}
	return best
}

func ciCauseDetail(g *ciGroup) string {
	where := "on this host"
	switch n := len(g.repos); {
	case n == 1:
		where = "in " + g.heaviestRepo()
	case n > 1:
		where = fmt.Sprintf("across %d job directories (most in %s)", n, g.heaviestRepo())
	}
	return fmt.Sprintf("CI runner (%s) work %s leads the CPU table; top process %s (pid %d)", g.runner, where, g.top.Command, g.top.PID)
}
