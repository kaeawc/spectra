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

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/orphans"
	"github.com/kaeawc/spectra/internal/process"
)

// orphansDeps are the seams runOrphansWithIO uses so tests need no real host.
type orphansDeps struct {
	Procs        func(ctx context.Context) []process.Info
	Cwds         func(ctx context.Context, pids []int) map[int]string
	FS           hostload.FS
	Now          func() time.Time
	Getwd        func() (string, error)
	Pid          func() int
	Abs          func(string) (string, error)
	EvalSymlinks func(string) (string, error)
}

func defaultOrphansDeps() orphansDeps {
	d := defaultLoadDeps()
	return orphansDeps{
		Procs: d.Procs, Cwds: d.Host.Cwds, FS: d.FS, Now: d.Now,
		Getwd: os.Getwd, Pid: os.Getpid, Abs: filepath.Abs, EvalSymlinks: filepath.EvalSymlinks,
	}
}

var orphansLimitations = []string{
	"PPID 1 is a heuristic: per-user launchd/systemd services also have PPID 1 and can appear here.",
	"Processes whose working directory cannot be read (other users) are skipped and counted.",
}

type orphansReport struct {
	Roots       []string        `json:"roots"`
	Groups      []orphans.Group `json:"groups"`
	Total       int             `json:"total"`
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
the working directory is under a root. Nothing is ever killed; the pid list is
for you to review and pass to kill yourself.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Exit codes:
  0  no orphans found
  3  orphans found
  1  runtime or output error
  2  usage
`)
	}
	asJSON := fs.Bool("json", false, "Emit JSON instead of text")
	minAge := fs.Duration("min-age", time.Minute, "Ignore processes younger than this")
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
		Roots: resolved, MinAge: *minAge, Now: deps.Now(), SelfPID: deps.Pid(), FS: deps.FS,
	})
	rep := orphansReport{Roots: []string(roots), Groups: res.Groups, Total: res.Total, Skipped: res.Skipped, Limitations: orphansLimitations}
	if rep.Groups == nil {
		rep.Groups = []orphans.Group{}
	}
	if err := writeOrphans(stdout, rep, res.PIDs(), *asJSON); err != nil {
		fmt.Fprintf(stderr, "spectra orphans: write output: %v\n", err)
		return 1
	}
	if res.Total > 0 {
		return 3
	}
	return 0
}

func writeOrphans(w io.Writer, rep orphansReport, pids []int, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	ew := &errWriter{w: w}
	for _, g := range rep.Groups {
		fmt.Fprintf(ew, "%s  (root %s)  %d processes  %.0f MB\n", g.Worktree, g.Root, len(g.Processes), float64(g.RSSKiB)/1024)
		for _, p := range g.Processes {
			missing := ""
			if p.CwdMissing {
				missing = "  [cwd missing]"
			}
			fmt.Fprintf(ew, "  %6d  %-10s  %8s  %7.0f MB  %5.1f%% cpu  %s  %s  +%d descendants, %.0f MB%s\n",
				p.PID, p.User, (time.Duration(p.AgeSeconds) * time.Second).String(), float64(p.RSSKiB)/1024, p.CPUPct,
				p.Command, p.Cwd, p.Descendants, float64(p.SubtreeRSSKiB)/1024, missing)
		}
	}
	fmt.Fprintf(ew, "%d candidate processes", rep.Total)
	if rep.Skipped > 0 {
		fmt.Fprintf(ew, ", %d skipped (cwd unreadable)", rep.Skipped)
	}
	fmt.Fprintln(ew)
	for _, l := range rep.Limitations {
		fmt.Fprintf(ew, "note: %s\n", l)
	}
	if len(pids) > 0 {
		strs := make([]string, len(pids))
		for i, p := range pids {
			strs[i] = strconv.Itoa(p)
		}
		fmt.Fprintf(ew, "pids: %s\n", strings.Join(strs, " "))
	}
	return ew.err
}
