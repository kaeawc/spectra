package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/orphans"
)

// orphansDeps are the seams runOrphansWithIO uses so tests need no real host.
type orphansDeps = orphans.ReportDeps

type orphansReport = orphans.Report

func defaultOrphansDeps() orphansDeps { return orphans.DefaultReportDeps() }

const originNoDaemon = orphans.OriginNoDaemon

func originLookupError(err error) string { return orphans.OriginLookupError(err) }

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
	minAge := fs.Duration("min-age", orphans.DefaultMinAge, "Ignore processes younger than this")
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
	rep, err := orphans.BuildReport(context.Background(), orphans.ReportOptions{Roots: roots, MinAge: *minAge, NoDaemon: *noDaemon}, deps)
	if err != nil {
		fmt.Fprintf(stderr, "spectra orphans: %v\n", err)
		return 1
	}
	if err := writeOrphans(stdout, rep, rep.PIDs(), *asJSON, *showPIDs); err != nil {
		fmt.Fprintf(stderr, "spectra orphans: write output: %v\n", err)
		return 1
	}
	if rep.Total > 0 {
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
