package orphans

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawnorigin"
	"github.com/kaeawc/spectra/internal/watchclient"
)

// Report is the `spectra orphans --json` document; the MCP orphans tool
// returns the same value.
type Report struct {
	Roots       []string `json:"roots"`
	Groups      []Group  `json:"groups"`
	Total       int      `json:"total"`
	DaemonTotal int      `json:"daemon_by_design_total"`
	Skipped     int      `json:"skipped_unreadable_cwd"`
	Limitations []string `json:"limitations"`
}

// PIDs lists the non-daemon candidates in the report.
func (r Report) PIDs() []int { return Result{Groups: r.Groups}.PIDs() }

// Limitations is the caveat list every report carries.
var Limitations = []string{
	"PPID 1 is a heuristic: per-user launchd/systemd services, tools that daemonize on purpose, and anything started with nohup/disown also have PPID 1.",
	"This list is evidence, not a kill list: a candidate with descendants may be the parent of live work.",
	"Processes whose working directory cannot be read (other users) are skipped and counted.",
	"Origins come from the running daemon's spawn probe (every 5s by default, held in memory only): a process whose parent exited within one probe, or that started before the daemon, has an unknown origin.",
}

// ReportOptions are the orphans scan's inputs (the CLI's flags).
type ReportOptions struct {
	// Roots are directories holding worktrees; empty means the current
	// directory (deps.Getwd).
	Roots  []string
	MinAge time.Duration
	// NoDaemon skips asking the running daemon for spawn origins.
	NoDaemon bool
}

// DefaultMinAge is the CLI's --min-age default.
const DefaultMinAge = time.Minute

// ReportDeps are the seams BuildReport uses so tests need no real host.
type ReportDeps struct {
	Procs func(ctx context.Context) []process.Info
	// Cwds must keep the Linux " (deleted)" suffix so orphans can see removed directories.
	Cwds         func(ctx context.Context, pids []int) map[int]string
	Stat         func(string) error
	Now          func() time.Time
	Getwd        func() (string, error)
	Pid          func() int
	Abs          func(string) (string, error)
	EvalSymlinks func(string) (string, error)
	// Origins asks the running daemon for recorded spawn origins; nil
	// means no daemon, so every origin is unknown.
	Origins func(ctx context.Context, queries []spawnorigin.Query) OriginSource
	// Roles classifies recorded ancestors; nil leaves roles empty.
	Roles RoleFunc
}

// DefaultReportDeps reads the real host and asks an already-running daemon
// for origins; it never starts one.
func DefaultReportDeps() ReportDeps {
	d := hostload.DefaultReportDeps()
	return ReportDeps{
		Procs: d.Procs, Cwds: d.Host.CwdsRaw, Stat: lstat, Now: d.Now,
		Getwd: os.Getwd, Pid: os.Getpid, Abs: filepath.Abs, EvalSymlinks: filepath.EvalSymlinks,
		Origins: QueryDaemonOrigins, Roles: ArgvRoles,
	}
}

func lstat(p string) error {
	_, err := os.Lstat(p)
	return err
}

// OriginNoDaemon is the origin reason when no daemon answered.
const OriginNoDaemon = "spectra daemon is not running, so no spawn origins were recorded"

// QueryDaemonOrigins asks an already-running daemon; it never starts one.
func QueryDaemonOrigins(ctx context.Context, queries []spawnorigin.Query) OriginSource {
	if watchclient.Disabled(os.Getenv) {
		return OriginSource{Unavailable: "daemon access is disabled (SPECTRA_NO_DAEMON)"}
	}
	paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
	if err != nil {
		return OriginSource{Unavailable: OriginNoDaemon}
	}
	c, ok := watchclient.Connect(ctx, os.Getenv, paths)
	if !ok {
		return OriginSource{Unavailable: OriginNoDaemon}
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var all spawnorigin.Response
	for len(queries) > 0 {
		n := min(len(queries), spawnorigin.MaxQueries)
		resp, err := c.Origins(ctx, queries[:n])
		if err != nil {
			return OriginSource{Unavailable: OriginLookupError(err)}
		}
		answers := append(all.Answers, resp.Answers...)
		all, all.Answers = resp, answers
		queries = queries[n:]
	}
	return OriginSource{Response: all}
}

// OriginLookupError explains a failed daemon origin lookup.
func OriginLookupError(err error) string {
	var rpcErr *daemonclient.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == daemon.CodeMethodNotFound {
		return "the running daemon predates spawn-origin recording; restart it to record origins"
	}
	return fmt.Sprintf("daemon origin lookup failed: %v", err)
}

// ArgvRoles classifies from the recorded name and argv only: dead ancestors
// have no executable to inspect, and skipping it keeps codesign out.
func ArgvRoles(inputs []classify.Input) []classify.Role {
	res, _ := classify.New(classify.Options{}).Classify(context.Background(), inputs)
	out := make([]classify.Role, len(res))
	for i, r := range res {
		out[i] = r.Role
	}
	return out
}

// BuildReport finds leftover processes under the roots. Nothing is killed.
func BuildReport(ctx context.Context, opts ReportOptions, deps ReportDeps) (Report, error) {
	roots := opts.Roots
	if len(roots) == 0 {
		wd, err := deps.Getwd()
		if err != nil {
			return Report{}, fmt.Errorf("current directory: %w", err)
		}
		roots = []string{wd}
	}
	procs := deps.Procs(ctx)
	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}
	resolved := hostload.ResolveRoots(roots, deps.Abs, deps.EvalSymlinks)
	res := Find(procs, deps.Cwds(ctx, pids), Options{
		Roots: resolved, MinAge: opts.MinAge, Now: deps.Now(), SelfPID: deps.Pid(), Stat: deps.Stat,
	})
	origins := OriginSource{Unavailable: OriginNoDaemon}
	switch {
	case opts.NoDaemon:
		origins.Unavailable = "daemon access is disabled (--no-daemon)"
	case deps.Origins != nil && res.Total+res.DaemonTotal > 0:
		origins = deps.Origins(ctx, Queries(res, procs))
	}
	AttachOrigins(&res, procs, origins, deps.Roles)
	rep := Report{Roots: roots, Groups: res.Groups, Total: res.Total, DaemonTotal: res.DaemonTotal, Skipped: res.Skipped, Limitations: Limitations}
	if rep.Groups == nil {
		rep.Groups = []Group{}
	}
	return rep, nil
}
