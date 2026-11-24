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

	"github.com/kaeawc/spectra/internal/idle"
)

const (
	idleExitNone  = 0
	idleExitError = 1
	idleExitUsage = 2
	idleExitFound = 3
)

// idleDeps are the seams runIdleWithIO uses so tests need no real host.
type idleDeps = idle.ReportDeps

type idleReport = idle.Report

func defaultIdleDeps() idleDeps { return idle.DefaultReportDeps() }

type idleFlags struct {
	asJSON   bool
	minAge   time.Duration
	maxCPU   float64
	window   time.Duration
	timeout  time.Duration
	pids     string
	roots    stringList
	noDaemon bool
}

func runIdle(args []string) int {
	return runIdleVia(args, os.Stdout, os.Stderr, defaultIdleDeps(), callDaemonReport)
}

func parseIdleFlags(args []string, stderr io.Writer) (idleFlags, bool) {
	var f idleFlags
	fs := flag.NewFlagSet("spectra idle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: spectra idle [--root DIR]... [--pids N,N] [flags]

List long-idle holders: processes whose working directory is under a root (or
that are named by --pids) that have been alive longer than --min-age while
they and all their descendants used under --max-cpu percent of one CPU, summed,
over a --window sample. Each is shown with its parent chain, its descendants,
and what it is waiting on where that is cheap to read (Linux wchan; open files
named like locks). Evidence only; nothing is ever killed.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Exit codes:
  0  no idle holders found
  3  idle holders found
  1  runtime or output error (including reaching --timeout, CPU that could
     not be measured over a window, and a --root that is not a directory)
  2  usage
`)
	}
	fs.BoolVar(&f.asJSON, "json", false, "Emit JSON instead of text")
	def := idle.DefaultReportOptions()
	fs.DurationVar(&f.minAge, "min-age", def.MinAge, "Only processes alive at least this long")
	fs.Float64Var(&f.maxCPU, "max-cpu", def.MaxCPU, "Idle means the process plus descendants used under this percent of one CPU")
	fs.DurationVar(&f.window, "window", def.Window, "CPU sample window")
	fs.DurationVar(&f.timeout, "timeout", def.Timeout, "Give up (exit 1) after this long")
	fs.StringVar(&f.pids, "pids", "", "Comma-separated pids to check (with or instead of --root)")
	fs.Var(&f.roots, "root", "Directory whose processes are checked (repeatable; default: the current directory unless --pids)")
	fs.BoolVar(&f.noDaemon, "no-daemon", false, "Measure in this process instead of asking the running daemon (also SPECTRA_NO_DAEMON=1)")
	if err := fs.Parse(args); err != nil {
		return f, false
	}
	if fs.NArg() > 0 || f.options(nil).Validate() != nil {
		fmt.Fprintln(stderr, "spectra idle: invalid arguments (need --max-cpu > 0, --window >= 100ms, --timeout > --window; see --help)")
		return f, false
	}
	return f, true
}

func runIdleWithIO(args []string, stdout, stderr io.Writer, deps idleDeps) int {
	return runIdleVia(args, stdout, stderr, deps, nil)
}

// runIdleVia asks the daemon through call first, when non-nil; its CPU
// tracker answers without sleeping through the window.
func runIdleVia(args []string, stdout, stderr io.Writer, deps idleDeps, call reportCaller) int {
	f, ok := parseIdleFlags(args, stderr)
	if !ok {
		return idleExitUsage
	}
	pids, err := parsePIDList(f.pids)
	if err != nil {
		fmt.Fprintf(stderr, "spectra idle: %v\n", err)
		return idleExitUsage
	}
	if f.noDaemon {
		call = nil
	}
	rep, ok := idleViaDaemon(call, f.options(pids), stderr)
	if !ok {
		if rep, err = idle.BuildReport(context.Background(), f.options(pids), deps); err != nil {
			fmt.Fprintf(stderr, "spectra idle: %v\n", err)
			return idleExitError
		}
	}
	if f.asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, err)
			return idleExitError
		}
	} else {
		printIdle(stdout, rep)
	}
	if len(rep.Result.Holders) > 0 {
		return idleExitFound
	}
	return idleExitNone
}

func (f idleFlags) options(pids []int) idle.ReportOptions {
	return idle.ReportOptions{Roots: f.roots, PIDs: pids, MinAge: f.minAge, MaxCPU: f.maxCPU, Window: f.window, Timeout: f.timeout}
}

func printIdle(w io.Writer, rep idleReport) {
	res := rep.Result
	scope := strings.Join(rep.Roots, ", ")
	if len(rep.PIDs) > 0 {
		if scope != "" {
			scope += " and "
		}
		scope += "pids " + joinInts(rep.PIDs)
	}
	if len(res.Holders) == 0 {
		fmt.Fprintf(w, "No idle holders under %s (checked %d: %d busy, %d younger than %ds, %d unmeasured).\n",
			scope, res.Checked, res.Busy, res.Young, rep.MinAgeSeconds, res.Unmeasured)
	} else {
		fmt.Fprintf(w, "Idle holders under %s (alive >= %ds, tree CPU < %.2f%% over %dms):\n", scope, rep.MinAgeSeconds, rep.MaxCPUPct, rep.WindowMS)
	}
	for _, h := range res.Holders {
		printIdleHolder(w, h)
	}
	if len(res.MissingPIDs) > 0 {
		fmt.Fprintf(w, "Not running: %s.\n", joinInts(res.MissingPIDs))
	}
	if rep.Incomplete {
		fmt.Fprintf(w, "Note: result may be incomplete: %s.\n", rep.Hint)
	}
}

func printIdleHolder(w io.Writer, h idle.Holder) {
	fmt.Fprintf(w, "  %d  %s  %s  age %s  cpu %.2f%% (tree %.2f%%)\n", h.PID, orDash(h.User), orDash(h.Command),
		(time.Duration(h.AgeSeconds) * time.Second).String(), h.CPUPct, h.TreeCPUPct)
	if h.Cwd != "" {
		fmt.Fprintf(w, "      cwd: %s\n", h.Cwd)
	}
	if h.CommandLine != "" {
		fmt.Fprintf(w, "      cmd: %s\n", truncate(h.CommandLine, 120))
	}
	printIdleWait(w, "      ", h.Wait)
	if len(h.Chain) > 0 {
		parts := make([]string, len(h.Chain))
		for i, l := range h.Chain {
			parts[i] = fmt.Sprintf("%s (%d)", l.Command, l.PID)
		}
		fmt.Fprintf(w, "      parents: %s\n", strings.Join(parts, " <- "))
	}
	for _, d := range h.Descendants {
		fmt.Fprintf(w, "      child %d  %s  cpu %.2f%%\n", d.PID, orDash(d.Command), d.CPUPct)
		printIdleWait(w, "        ", d.Wait)
	}
	if h.DescendantsTotal > len(h.Descendants) {
		fmt.Fprintf(w, "      ... %d more descendants\n", h.DescendantsTotal-len(h.Descendants))
	}
}

func printIdleWait(w io.Writer, indent string, wt *idle.Wait) {
	if wt == nil {
		return
	}
	if wt.WChan != "" {
		fmt.Fprintf(w, "%swaiting in: %s\n", indent, wt.WChan)
	}
	for _, l := range wt.LockFiles {
		fmt.Fprintf(w, "%slock file: %s\n", indent, l)
	}
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}
