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
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const (
	idleExitNone  = 0
	idleExitError = 1
	idleExitUsage = 2
	idleExitFound = 3
)

// idleDeps are the seams runIdleWithIO uses so tests need no real host.
type idleDeps struct {
	Sample func(ctx context.Context, window time.Duration) (idle.Sample, error)
	// Cwds returns working directories and whether some could not be read.
	Cwds  func(ctx context.Context, procs []process.Info) (map[int]string, bool, error)
	Waits func(ctx context.Context, pids []int) map[int]*idle.Wait
	// Under matches a cwd against a root's spellings; nil is path-only.
	Under        func(cwd string, roots []string) bool
	Stat         func(string) (os.FileInfo, error)
	Now          func() time.Time
	Pid          func() int
	Getwd        func() (string, error)
	Abs          func(string) (string, error)
	EvalSymlinks func(string) (string, error)
}

func defaultIdleDeps() idleDeps {
	osk := hostos.Resolve(hostos.Unknown)
	collect := func(ctx context.Context) []process.Info {
		return process.CollectAll(ctx, process.CollectOptions{
			OS: osk, Runner: proc.Default,
			DetailCollector: func([]process.Info) map[int]process.Details { return map[int]process.Details{} },
		})
	}
	sampler := idle.HostSampler{OS: osk, Runner: proc.Default, Collect: collect, Now: time.Now, Sleep: sleepCtx}
	finder := owner.Finder{OS: osk}
	return idleDeps{
		Sample:       sampler.Sample,
		Cwds:         finder.Cwds,
		Waits:        idle.WaitReader{OS: osk}.Read,
		Under:        finder.NewCwdMatcher().Under,
		Stat:         os.Stat,
		Now:          time.Now,
		Pid:          os.Getpid,
		Getwd:        os.Getwd,
		Abs:          filepath.Abs,
		EvalSymlinks: filepath.EvalSymlinks,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

var idleLimitations = []string{
	"Evidence only: nothing is signalled. A process that is legitimately waiting (a server between requests, an editor, a shell) also looks idle.",
	"CPU is summed over the process and all its descendants for one window; a descendant started during the window counts as activity.",
	"A process whose CPU could not be measured, or whose working directory could not be read, is never reported.",
	"Spectra, its ancestors and its descendants are excluded.",
}

type idleReport struct {
	Roots         []string    `json:"roots,omitempty"`
	PIDs          []int       `json:"pids,omitempty"`
	MinAgeSeconds int64       `json:"min_age_seconds"`
	MaxCPUPct     float64     `json:"max_cpu_pct"`
	WindowMS      int64       `json:"window_ms"`
	CPUBasis      string      `json:"cpu_basis"`
	Incomplete    bool        `json:"incomplete,omitempty"`
	Hint          string      `json:"hint,omitempty"`
	Result        idle.Result `json:"result"`
	Limitations   []string    `json:"limitations"`
}

type idleFlags struct {
	asJSON  bool
	minAge  time.Duration
	maxCPU  float64
	window  time.Duration
	timeout time.Duration
	pids    string
	roots   stringList
}

func runIdle(args []string) int {
	return runIdleWithIO(args, os.Stdout, os.Stderr, defaultIdleDeps())
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
	fs.DurationVar(&f.minAge, "min-age", 10*time.Minute, "Only processes alive at least this long")
	fs.Float64Var(&f.maxCPU, "max-cpu", 1, "Idle means the process plus descendants used under this percent of one CPU")
	fs.DurationVar(&f.window, "window", 5*time.Second, "CPU sample window")
	fs.DurationVar(&f.timeout, "timeout", time.Minute, "Give up (exit 1) after this long")
	fs.StringVar(&f.pids, "pids", "", "Comma-separated pids to check (with or instead of --root)")
	fs.Var(&f.roots, "root", "Directory whose processes are checked (repeatable; default: the current directory unless --pids)")
	if err := fs.Parse(args); err != nil {
		return f, false
	}
	if fs.NArg() > 0 || f.minAge < 0 || f.maxCPU <= 0 || f.window < 100*time.Millisecond || f.timeout <= f.window {
		fmt.Fprintln(stderr, "spectra idle: invalid arguments (need --max-cpu > 0, --window >= 100ms, --timeout > --window; see --help)")
		return f, false
	}
	return f, true
}

func runIdleWithIO(args []string, stdout, stderr io.Writer, deps idleDeps) int {
	f, ok := parseIdleFlags(args, stderr)
	if !ok {
		return idleExitUsage
	}
	pids, err := parsePIDList(f.pids)
	if err != nil {
		fmt.Fprintf(stderr, "spectra idle: %v\n", err)
		return idleExitUsage
	}
	if len(f.roots) == 0 && len(pids) == 0 {
		wd, err := deps.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "spectra idle: current directory: %v\n", err)
			return idleExitError
		}
		f.roots = stringList{wd}
	}
	if err := checkIdleRoots(f.roots, deps); err != nil {
		fmt.Fprintf(stderr, "spectra idle: %v\n", err)
		return idleExitError
	}
	rep, err := collectIdle(f, pids, deps)
	if err != nil {
		fmt.Fprintf(stderr, "spectra idle: %v\n", err)
		return idleExitError
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

// checkIdleRoots rejects a root that is not an existing directory: matching
// nothing under it would read as "no idle holders".
func checkIdleRoots(roots []string, deps idleDeps) error {
	if deps.Stat == nil {
		return nil
	}
	for _, r := range roots {
		fi, err := deps.Stat(r)
		if err != nil {
			return fmt.Errorf("--root %s: %w", r, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("--root %s is not a directory", r)
		}
	}
	return nil
}

func collectIdle(f idleFlags, pids []int, deps idleDeps) (idleReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), f.timeout)
	defer cancel()
	s, err := deps.Sample(ctx, f.window)
	if err != nil {
		return idleReport{}, err
	}
	if s.Basis != idle.BasisWindow || s.Window <= 0 {
		return idleReport{}, fmt.Errorf("CPU could not be measured over a window (basis %q), so idleness is unknown", s.Basis)
	}
	if len(s.Procs) == 0 {
		return idleReport{}, errors.New("the process list is empty (ps or /proc could not be read)")
	}
	rep := idleReport{
		Roots: []string(f.roots), PIDs: pids, MinAgeSeconds: int64(f.minAge / time.Second), MaxCPUPct: f.maxCPU,
		WindowMS: s.Window.Milliseconds(), CPUBasis: s.Basis, Limitations: idleLimitations,
	}
	var cwds map[int]string
	if len(f.roots) > 0 {
		var denied bool
		cwds, denied, err = deps.Cwds(ctx, s.Procs)
		if err != nil {
			return idleReport{}, err
		}
		if denied {
			rep.Incomplete = true
			rep.Hint = "some processes' working directories could not be read (other users); re-run with sudo"
		}
	}
	rep.Result = idle.Find(s, cwds, idle.Options{
		Roots: idleRoots(f.roots, deps), PIDs: pids, MinAge: f.minAge, MaxCPUPct: f.maxCPU,
		Now: deps.Now(), SelfPID: deps.Pid(), Under: deps.Under,
	})
	if len(rep.Result.Holders) > 0 {
		rep.Result.AttachWaits(deps.Waits(ctx, rep.Result.Pids()))
	}
	if err := ctx.Err(); err != nil {
		return idleReport{}, fmt.Errorf("gave up after --timeout %s: %w", f.timeout, err)
	}
	return rep, nil
}

func idleRoots(roots []string, deps idleDeps) []idle.Root {
	var out []idle.Root
	for _, r := range resolveRoots(roots, deps.Abs, deps.EvalSymlinks) {
		path := r.Path
		if a, err := deps.Abs(path); err == nil {
			path = filepath.Clean(a)
		}
		out = append(out, idle.Root{Path: path, Resolved: r.Resolved})
	}
	return out
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
