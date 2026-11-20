package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spawnorigin"
	"github.com/kaeawc/spectra/internal/watchclient"
)

// orphansDeps are the seams runOrphansWithIO uses so tests need no real host.
type orphansDeps struct {
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
	Origins func(ctx context.Context, queries []spawnorigin.Query) orphans.OriginSource
	// Roles classifies recorded ancestors; nil leaves roles empty.
	Roles orphans.RoleFunc
}

func lstat(p string) error {
	_, err := os.Lstat(p)
	return err
}

func defaultOrphansDeps() orphansDeps {
	d := defaultLoadDeps()
	return orphansDeps{
		Procs: d.Procs, Cwds: d.Host.CwdsRaw, Stat: lstat, Now: d.Now,
		Getwd: os.Getwd, Pid: os.Getpid, Abs: filepath.Abs, EvalSymlinks: filepath.EvalSymlinks,
		Origins: queryDaemonOrigins, Roles: argvRoles,
	}
}

const originNoDaemon = "spectra daemon is not running, so no spawn origins were recorded"

// queryDaemonOrigins asks an already-running daemon; it never starts one.
func queryDaemonOrigins(ctx context.Context, queries []spawnorigin.Query) orphans.OriginSource {
	if watchclient.Disabled(os.Getenv) {
		return orphans.OriginSource{Unavailable: "daemon access is disabled (SPECTRA_NO_DAEMON)"}
	}
	paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
	if err != nil {
		return orphans.OriginSource{Unavailable: originNoDaemon}
	}
	c, ok := watchclient.Connect(ctx, os.Getenv, paths)
	if !ok {
		return orphans.OriginSource{Unavailable: originNoDaemon}
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var all spawnorigin.Response
	for len(queries) > 0 {
		n := min(len(queries), spawnorigin.MaxQueries)
		resp, err := c.Origins(ctx, queries[:n])
		if err != nil {
			return orphans.OriginSource{Unavailable: originLookupError(err)}
		}
		answers := append(all.Answers, resp.Answers...)
		all, all.Answers = resp, answers
		queries = queries[n:]
	}
	return orphans.OriginSource{Response: all}
}

func originLookupError(err error) string {
	var rpcErr *daemonclient.RPCError
	if errors.As(err, &rpcErr) && rpcErr.Code == daemon.CodeMethodNotFound {
		return "the running daemon predates spawn-origin recording; restart it to record origins"
	}
	return fmt.Sprintf("daemon origin lookup failed: %v", err)
}

// argvRoles classifies from the recorded name and argv only: dead ancestors
// have no executable to inspect, and skipping it keeps codesign out.
func argvRoles(inputs []classify.Input) []classify.Role {
	res, _ := classify.New(classify.Options{}).Classify(context.Background(), inputs)
	out := make([]classify.Role, len(res))
	for i, r := range res {
		out[i] = r.Role
	}
	return out
}

var orphansLimitations = []string{
	"PPID 1 is a heuristic: per-user launchd/systemd services, tools that daemonize on purpose, and anything started with nohup/disown also have PPID 1.",
	"This list is evidence, not a kill list: a candidate with descendants may be the parent of live work.",
	"Processes whose working directory cannot be read (other users) are skipped and counted.",
	"Origins come from the running daemon's spawn probe (every 5s by default, held in memory only): a process whose parent exited within one probe, or that started before the daemon, has an unknown origin.",
}

type orphansReport struct {
	Roots       []string        `json:"roots"`
	Groups      []orphans.Group `json:"groups"`
	Total       int             `json:"total"`
	DaemonTotal int             `json:"daemon_by_design_total"`
	Skipped     int             `json:"skipped_unreadable_cwd"`
	Limitations []string        `json:"limitations"`
}

func runOrphans(args []string) int {
	return runOrphansWithIO(args, os.Stdout, os.Stderr, defaultOrphansDeps())
}

func runOrphansWithIO(args []string, stdout, stderr io.Writer, deps orphansDeps) int {
	fs := flag.NewFlagSet("spectra orphans", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: spectra orphans [flags]

Find leftover processes from worktrees and finished jobs: parent is pid 1 and
the working directory is under a root. Nothing is ever killed. PPID 1 also
covers tools that daemonize on purpose and anything started with nohup/disown,
so the list is evidence, not a kill list, and a candidate with descendants may
be the parent of live work. Known daemonize-by-design tools are listed
separately and left out of the exit code and --pids.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Exit codes:
  0  no orphans found
  3  non-daemon orphans found
  1  runtime or output error
  2  usage
`)
	}
	asJSON := fs.Bool("json", false, "Emit JSON instead of text")
	minAge := fs.Duration("min-age", time.Minute, "Ignore processes younger than this")
	showPIDs := fs.Bool("pids", false, "Print a space-separated pid line for non-daemon candidates (review before use)")
	noDaemon := fs.Bool("no-daemon", false, "Do not ask the running daemon for recorded spawn origins")
	var roots stringList
	fs.Var(&roots, "root", "Directory holding worktrees (repeatable; default: the current directory)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *minAge < 0 {
		fmt.Fprintln(stderr, "spectra orphans: invalid arguments (see --help)")
		return 2
	}
	if len(roots) == 0 {
		wd, err := deps.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "spectra orphans: current directory: %v\n", err)
			return 1
		}
		roots = stringList{wd}
	}

	ctx := context.Background()
	procs := deps.Procs(ctx)
	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}
	resolved := resolveRoots(roots, deps.Abs, deps.EvalSymlinks)
	res := orphans.Find(procs, deps.Cwds(ctx, pids), orphans.Options{
		Roots: resolved, MinAge: *minAge, Now: deps.Now(), SelfPID: deps.Pid(), Stat: deps.Stat,
	})
	origins := orphans.OriginSource{Unavailable: originNoDaemon}
	switch {
	case *noDaemon:
		origins.Unavailable = "daemon access is disabled (--no-daemon)"
	case deps.Origins != nil && res.Total+res.DaemonTotal > 0:
		origins = deps.Origins(ctx, orphans.Queries(res, procs))
	}
	orphans.AttachOrigins(&res, procs, origins, deps.Roles)
	rep := orphansReport{Roots: []string(roots), Groups: res.Groups, Total: res.Total, DaemonTotal: res.DaemonTotal, Skipped: res.Skipped, Limitations: orphansLimitations}
	if rep.Groups == nil {
		rep.Groups = []orphans.Group{}
	}
	if err := writeOrphans(stdout, rep, res.PIDs(), *asJSON, *showPIDs); err != nil {
		fmt.Fprintf(stderr, "spectra orphans: write output: %v\n", err)
		return 1
	}
	if res.Total > 0 {
		return 3
	}
	return 0
}

func writeOrphans(w io.Writer, rep orphansReport, pids []int, asJSON, showPIDs bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	ew := &errWriter{w: w}
	for _, g := range rep.Groups {
		writeOrphanGroup(ew, g)
	}
	writeOrphanDaemons(ew, rep.Groups)
	fmt.Fprintf(ew, "%d candidate processes", rep.Total)
	if rep.DaemonTotal > 0 {
		fmt.Fprintf(ew, ", %d probably intentional", rep.DaemonTotal)
	}
	if rep.Skipped > 0 {
		fmt.Fprintf(ew, ", %d skipped (cwd unreadable)", rep.Skipped)
	}
	fmt.Fprintln(ew)
	for _, l := range rep.Limitations {
		fmt.Fprintf(ew, "note: %s\n", l)
	}
	if showPIDs && len(pids) > 0 {
		strs := make([]string, len(pids))
		for i, p := range pids {
			strs[i] = strconv.Itoa(p)
		}
		fmt.Fprintf(ew, "pids: %s\n", strings.Join(strs, " "))
	}
	return ew.err
}

func writeOrphanGroup(ew io.Writer, g orphans.Group) {
	var rows []orphans.Process
	for _, p := range g.Processes {
		if !p.DaemonByDesign {
			rows = append(rows, p)
		}
	}
	if len(rows) == 0 {
		return
	}
	removed := ""
	if g.WorktreeRemoved {
		removed = "  [worktree removed]"
	}
	fmt.Fprintf(ew, "%s%s  (root %s)  %d processes  %.0f MB incl. descendants\n",
		g.Worktree, removed, g.Root, len(rows), float64(g.RSSKiB)/1024)
	for _, p := range rows {
		writeOrphanRow(ew, p)
	}
}

func writeOrphanDaemons(ew io.Writer, groups []orphans.Group) {
	header := false
	for _, g := range groups {
		for _, p := range g.Processes {
			if !p.DaemonByDesign {
				continue
			}
			if !header {
				fmt.Fprintln(ew, "Probably intentional (daemonize by design):")
				header = true
			}
			writeOrphanRow(ew, p)
		}
	}
}

const orphanCmdMax = 120

func truncateCommand(s string) string {
	r := []rune(s)
	if len(r) <= orphanCmdMax {
		return s
	}
	return string(r[:orphanCmdMax-1]) + "\u2026"
}

func writeOrphanRow(ew io.Writer, p orphans.Process) {
	flag := ""
	switch {
	case p.CwdMissing:
		flag = "  [cwd missing]"
	case p.CwdUnreadable:
		flag = "  [cwd unreadable]"
	}
	fmt.Fprintf(ew, "  %6d  %-10s  %8s  %7.0f MB  %5.1f%% cpu  %s  %s  +%d descendants, %.0f MB%s\n",
		p.PID, p.User, (time.Duration(p.AgeSeconds) * time.Second).String(), float64(p.RSSKiB)/1024, p.CPUPct,
		truncateCommand(p.CommandLine), p.Cwd, p.Descendants, float64(p.SubtreeRSSKiB)/1024, flag)
	if o := p.Origin; o != nil && o.Known {
		fmt.Fprintf(ew, "          origin: %s", describeAncestor(*o.Parent))
		if o.Session != nil && o.Session != o.Parent && o.Session != o.Agent {
			fmt.Fprintf(ew, "; session: %s", describeAncestor(*o.Session))
		}
		if o.Agent != nil && o.Agent != o.Parent {
			fmt.Fprintf(ew, "; agent: %s", describeAncestor(*o.Agent))
		}
		fmt.Fprintln(ew)
	}
}

func describeAncestor(a orphans.Ancestor) string {
	cmd := a.CommandLine
	if cmd == "" {
		cmd = a.Command
	}
	s := fmt.Sprintf("%s (pid %d", truncateCommand(cmd), a.PID)
	if a.Role != "" {
		s += ", " + a.Role
	}
	switch {
	case a.Alive:
		s += ", alive"
	case a.ExitedAt != nil:
		s += ", exited by " + a.ExitedAt.Local().Format("2006-01-02 15:04:05")
	}
	s += ")"
	if a.Cwd != "" {
		s += " in " + a.Cwd
	}
	return s
}
