package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/nettop"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const networkTopUsage = "usage: spectra network top [--duration 10s] [--interval 1s] [--top 10] [--pid N] [--json]"

// networkTopDeps are the seams runNetworkTopWithIO needs; tests replace them.
type networkTopDeps struct {
	os  hostos.Kind
	run netstate.CmdRunner
	// runner runs the macOS measuring tool, which must die on Ctrl-C.
	runner proc.Runner
	procs  func(ctx context.Context) []process.Info
	conns  func() []netstate.Connection
	cwds   func(ctx context.Context, pids []int) map[int]string
	sleep  func(ctx context.Context, d time.Duration) error
	clock  clock.Clock
}

func defaultNetworkTopDeps() networkTopDeps {
	return networkTopDeps{
		run:    netstate.DefaultRunner,
		runner: proc.Default,
		procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{
				DetailCollector: func([]process.Info) map[int]process.Details { return nil },
				MemoryBytes:     func() uint64 { return 0 },
			})
		},
		conns: func() []netstate.Connection { return netstate.CollectConnections(netstate.DefaultRunner) },
		cwds:  hostload.Host{}.Cwds,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
		clock: clock.Default,
	}
}

type networkTopOpts struct {
	duration, interval time.Duration
	top, pid           int
	json               bool
}

func runNetworkTop(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runNetworkTopWithIO(ctx, args, os.Stdout, os.Stderr, defaultNetworkTopDeps())
}

func parseNetworkTopFlags(args []string, stderr io.Writer) (networkTopOpts, bool) {
	var o networkTopOpts
	fs := flag.NewFlagSet("spectra network top", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, networkTopUsage+"\n\n"+
			"Show which processes are using the network right now: measure the bytes each\n"+
			"process moves every interval and rank processes by bytes moved over the\n"+
			"window. Ctrl-C ends early and still prints the summary (before the first interval\n"+
			"completes it prints an empty summary marked interrupted and exits 0).\n\n"+
			"macOS runs one `nettop -d -L 2` per interval, so --interval and --duration\n"+
			"are rounded up to whole seconds and an interval cut short by Ctrl-C is\n"+
			"dropped; remote endpoints are the connections open at the end of the window.\n"+
			"Linux diffs per-socket TCP counters from `ss -tinpH`.\n\n")
		fs.PrintDefaults()
	}
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "How long to sample (Ctrl-C stops early)")
	fs.DurationVar(&o.interval, "interval", time.Second, "Sampling interval")
	fs.IntVar(&o.top, "top", 10, "Number of processes to list")
	fs.IntVar(&o.pid, "pid", 0, "Only this pid and its descendants (per-interval lines: descendants alive at the start; summary: also those alive at the end)")
	fs.BoolVar(&o.json, "json", false, "Emit one JSON object")
	if err := fs.Parse(args); err != nil {
		return o, false
	}
	switch {
	case fs.NArg() != 0:
		fmt.Fprintf(stderr, "network top: unexpected argument %q\n%s\n", fs.Arg(0), networkTopUsage)
	case o.duration <= 0 || o.interval <= 0:
		fmt.Fprintln(stderr, "network top: --duration and --interval must be positive")
	case o.top <= 0:
		fmt.Fprintln(stderr, "network top: --top must be positive")
	case o.pid < 0:
		fmt.Fprintln(stderr, "network top: --pid must be positive")
	default:
		return o, true
	}
	return o, false
}

func runNetworkTopWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, deps networkTopDeps) int {
	opts, ok := parseNetworkTopFlags(args, stderr)
	if !ok {
		return 2
	}
	src, err := nettop.SourceFor(deps.os, deps.run, deps.runner)
	if err != nil {
		fmt.Fprintf(stderr, "network top: %v\n", err)
		return 1
	}
	before := deps.procs(ctx)
	scope := newTopScope(opts.pid)
	if opts.pid > 0 && !scope.extend(before) {
		fmt.Fprintf(stderr, "network top: pid %d not found in the process table\n", opts.pid)
		return 1
	}

	asked := opts.interval
	opts.interval, opts.duration = src.Round(opts.interval), src.Round(opts.duration)
	ew := &errWriter{w: stdout}
	cfg := nettop.Config{
		Sleep: deps.sleep, Clock: deps.clock, Interval: opts.interval, Duration: opts.duration,
		EmitInclude: scope.startInclude(),
	}
	src.Configure(&cfg)
	if !opts.json {
		printNetworkTopHeader(ew, src.Tool, opts, asked)
		cfg.Emit = func(t nettop.Tick) { printNetworkTopTick(ew, t) }
	}
	tr, err := nettop.Run(ctx, cfg)
	if errors.Is(err, nettop.ErrInterrupted) {
		return printInterruptedNetworkTop(ew, stderr, src, opts)
	}
	if err != nil {
		fmt.Fprintf(stderr, "network top: %v\n", err)
		return 1
	}

	// The window may have ended on Ctrl-C; enrichment still needs to run.
	post := context.WithoutCancel(ctx)
	after := deps.procs(post)
	scope.extend(after)
	rep := nettop.Summarize(tr, scope.include(), opts.top)
	rep.Basis, rep.RemotesBasis, rep.PID = src.Basis, nettop.RemotesBasisFor(src.Basis), opts.pid
	rep.Limitations = append(rep.Limitations, nettop.Limitations(src.Basis)...)
	if opts.pid > 0 {
		rep.Limitations = append(rep.Limitations, pidScopeNote)
	}
	enrichNetworkTop(post, &rep, deps, before, after)

	return writeNetworkTopReport(ew, stderr, rep, opts.json)
}

func writeNetworkTopReport(ew *errWriter, stderr io.Writer, rep nettop.Report, asJSON bool) int {
	if asJSON {
		enc := json.NewEncoder(ew)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep) // failures latch in ew
	} else {
		_ = printNetworkTopReport(ew, rep) // failures latch in ew
	}
	if ew.err != nil {
		fmt.Fprintf(stderr, "network top: write output: %v\n", ew.err)
		return 1
	}
	return 0
}

// interruptedNote is shown when Ctrl-C ended the window before any interval
// completed, so there is nothing to rank.
const interruptedNote = "interrupted before the first interval completed; no samples were taken"

// printInterruptedNetworkTop prints the zero-duration summary for a window
// cancelled before its first interval and exits 0.
func printInterruptedNetworkTop(ew *errWriter, stderr io.Writer, src nettop.Source, opts networkTopOpts) int {
	rep := nettop.Report{
		IntervalMS: opts.interval.Milliseconds(), Basis: src.Basis,
		RemotesBasis: nettop.RemotesBasisFor(src.Basis), PID: opts.pid,
		Processes: []nettop.Process{}, Appeared: []nettop.Ref{}, Disappeared: []nettop.Ref{},
		Limitations: []string{interruptedNote},
	}
	return writeNetworkTopReport(ew, stderr, rep, opts.json)
}

// pidScopeNote says which processes --pid counts where; text, JSON and help
// all describe the same rule.
const pidScopeNote = "--pid: per-interval lines count the target and the descendants alive at the start; the summary also counts descendants alive at the end, so a child that started and exited inside the window is not included"

// topScope is the --pid subtree: the target plus every descendant seen in
// any process table read. start freezes the subtree of the first read.
type topScope struct {
	pid   int
	pids  map[int]bool
	start map[int]bool
}

func newTopScope(pid int) *topScope { return &topScope{pid: pid, pids: map[int]bool{}} }

// extend adds the target's subtree in procs and reports whether the target
// was there.
func (s *topScope) extend(procs []process.Info) bool {
	if s.pid <= 0 {
		return true
	}
	res, err := lineage.Build(procs, s.pid)
	if err != nil {
		return false
	}
	var walk func(n *lineage.Node)
	walk = func(n *lineage.Node) {
		s.pids[n.PID] = true
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(res.Tree)
	return true
}

func (s *topScope) include() func(int) bool {
	if s.pid <= 0 {
		return nil
	}
	return func(pid int) bool { return s.pids[pid] }
}

// startInclude accepts the subtree as of now, unaffected by later extends.
func (s *topScope) startInclude() func(int) bool {
	if s.pid <= 0 {
		return nil
	}
	s.start = make(map[int]bool, len(s.pids))
	for pid := range s.pids {
		s.start[pid] = true
	}
	return func(pid int) bool { return s.start[pid] }
}

func printNetworkTopHeader(w io.Writer, tool string, opts networkTopOpts, asked time.Duration) {
	fmt.Fprintf(w, "Sampling %s every %s for %s (Ctrl-C to stop early)\n", tool, opts.interval, opts.duration)
	if opts.interval != asked {
		fmt.Fprintf(w, "  interval rounded up from %s: %s takes whole seconds\n", asked, tool)
	}
	if opts.pid > 0 {
		fmt.Fprintf(w, "  %s\n", pidScopeNote)
	}
}

// enrichNetworkTop fills user, full command name, cwd and remote endpoints,
// all best effort.
func enrichNetworkTop(ctx context.Context, rep *nettop.Report, deps networkTopDeps, before, after []process.Info) {
	if len(rep.Processes) == 0 {
		return
	}
	byPID := map[int]process.Info{}
	for _, list := range [][]process.Info{before, after} {
		for _, p := range list {
			byPID[p.PID] = p
		}
	}
	pids := make([]int, 0, len(rep.Processes))
	needRemotes := false
	for i := range rep.Processes {
		p := &rep.Processes[i]
		if info, ok := byPID[p.PID]; ok {
			p.User = info.User
			// nettop truncates names to 15 characters; take the process
			// table's name only when it extends the sampled one.
			if p.Command == "" || (len(info.Command) > len(p.Command) && strings.HasPrefix(info.Command, p.Command)) {
				p.Command = info.Command
			}
		}
		pids = append(pids, p.PID)
		needRemotes = needRemotes || len(p.Remotes) == 0
	}
	if deps.cwds != nil {
		cwds := deps.cwds(ctx, pids)
		for i := range rep.Processes {
			rep.Processes[i].Cwd = cwds[rep.Processes[i].PID]
		}
	}
	if needRemotes && deps.conns != nil {
		fillRemotes(rep, deps.conns())
	}
}

func fillRemotes(rep *nettop.Report, conns []netstate.Connection) {
	byPID := map[int][]string{}
	for pass := 0; pass < 2; pass++ { // established TCP first, then the rest
		for _, c := range conns {
			if c.RemoteAddr == "" || (c.State == "established") != (pass == 0) {
				continue
			}
			list := byPID[c.PID]
			if len(list) < nettop.MaxRemotes && !slices.Contains(list, c.RemoteAddr) {
				byPID[c.PID] = append(list, c.RemoteAddr)
			}
		}
	}
	missing := 0
	for i := range rep.Processes {
		p := &rep.Processes[i]
		if len(p.Remotes) == 0 {
			p.Remotes = byPID[p.PID]
		}
		if len(p.Remotes) == 0 {
			missing++
		}
	}
	if rep.RemotesBasis == nettop.RemotesEndSnapshot {
		rep.Limitations = append(rep.Limitations, fmt.Sprintf("remote endpoints are the connections open at the end of the window (one listing), not necessarily those that moved the bytes; %d listed process(es) had none open then", missing))
	} else if missing > 0 {
		rep.Limitations = append(rep.Limitations, fmt.Sprintf("%d listed process(es) had no busy socket; their remote endpoints come from one connection listing at the end", missing))
	}
}

func printNetworkTopTick(w io.Writer, t nettop.Tick) {
	fmt.Fprintf(w, "%7.1fs  in %12s  out %12s  %d active\n",
		float64(t.ElapsedMS)/1000, humanRate(t.InRate), humanRate(t.OutRate), t.Active)
}

func printNetworkTopReport(out io.Writer, r nettop.Report) error {
	w := &errWriter{w: out}
	fmt.Fprintf(w, "\nNetwork top over %.1fs (%d samples, basis %s)", float64(r.DurationMS)/1000, r.Samples, r.Basis)
	if r.PID > 0 {
		fmt.Fprintf(w, ", pid %d and descendants", r.PID)
	}
	t := r.Totals
	fmt.Fprintf(w, "\n  total: in %s (avg %s)  out %s (avg %s)  peak %s  %d active process(es)\n",
		humanSize(t.BytesIn), humanRate(t.AvgInRate), humanSize(t.BytesOut), humanRate(t.AvgOutRate), humanRate(t.PeakTotalRate), t.ActiveProcesses)
	if r.SamplesFailed > 0 || r.SamplesSkipped > 0 {
		fmt.Fprintf(w, "  samples: %d failed, %d skipped (a sample overran its deadline)\n", r.SamplesFailed, r.SamplesSkipped)
	}
	if r.Samples == 0 {
		fmt.Fprintf(w, "  %s\n", interruptedNote)
	} else if len(r.Processes) == 0 {
		fmt.Fprintln(w, "  no process moved any bytes in the window")
	} else {
		printNetworkTopTable(w, r.Processes, r.RemotesBasis)
	}
	printNetworkTopRefs(w, "appeared during the window", r.Appeared)
	printNetworkTopRefs(w, "gone by the end", r.Disappeared)
	if len(r.Limitations) > 0 {
		fmt.Fprintln(w, "Limitations:")
		for _, l := range r.Limitations {
			fmt.Fprintf(w, "  - %s\n", l)
		}
	}
	return w.err
}

func printNetworkTopTable(w io.Writer, ps []nettop.Process, remotesBasis string) {
	remotes := "REMOTES"
	if remotesBasis == nettop.RemotesEndSnapshot {
		remotes = "REMOTES (open at end of window)"
	}
	fmt.Fprintf(w, "\n  %7s  %-20s  %-10s  %9s  %9s  %11s  %11s  %11s  %11s  %s\n",
		"PID", "COMMAND", "USER", "IN", "OUT", "AVG IN", "AVG OUT", "PEAK IN", "PEAK OUT", remotes)
	for _, p := range ps {
		fmt.Fprintf(w, "  %7d  %-20s  %-10s  %9s  %9s  %11s  %11s  %11s  %11s  %s\n",
			p.PID, truncate(p.Command, 20), truncate(dashIfEmpty(p.User), 10), humanSize(p.BytesIn), humanSize(p.BytesOut),
			humanRate(p.AvgInRate), humanRate(p.AvgOutRate), humanRate(p.PeakInRate), humanRate(p.PeakOutRate),
			strings.Join(p.Remotes, " "))
		if p.Cwd != "" {
			fmt.Fprintf(w, "  %7s  cwd %s\n", "", p.Cwd)
		}
	}
	fmt.Fprintln(w)
}

func printNetworkTopRefs(w io.Writer, label string, refs []nettop.Ref) {
	if len(refs) == 0 {
		return
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, fmt.Sprintf("%s (%d)", r.Command, r.PID))
	}
	fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(parts, ", "))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanRate(r float64) string {
	const k = 1024.0
	switch {
	case r >= k*k*k:
		return fmt.Sprintf("%.1f GB/s", r/(k*k*k))
	case r >= k*k:
		return fmt.Sprintf("%.1f MB/s", r/(k*k))
	case r >= k:
		return fmt.Sprintf("%.1f KB/s", r/k)
	}
	return fmt.Sprintf("%.0f B/s", r)
}
