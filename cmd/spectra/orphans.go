package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/process"
)

// orphansDeps are the seams runOrphansWithIO uses so tests need no real host.
type orphansDeps struct {
	Procs        func(ctx context.Context) []process.Info
	Cwds         func(ctx context.Context, pids []int) map[int]string
	Stat         func(string) error
	Now          func() time.Time
	Getwd        func() (string, error)
	Pid          func() int
	Abs          func(string) (string, error)
	EvalSymlinks func(string) (string, error)
}

func lstat(p string) error {
	_, err := os.Lstat(p)
	return err
}

func defaultOrphansDeps() orphansDeps {
	d := defaultLoadDeps()
	return orphansDeps{
		Procs: d.Procs, Cwds: d.Host.Cwds, Stat: lstat, Now: d.Now,
		Getwd: os.Getwd, Pid: os.Getpid, Abs: filepath.Abs, EvalSymlinks: filepath.EvalSymlinks,
	}
}

var orphansLimitations = []string{
	"PPID 1 is a heuristic: per-user launchd/systemd services, tools that daemonize on purpose, and anything started with nohup/disown also have PPID 1.",
	"This list is evidence, not a kill list: a candidate with descendants may be the parent of live work.",
	"Processes whose working directory cannot be read (other users) are skipped and counted.",
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
}
