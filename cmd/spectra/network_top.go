package main

import (
	"context"
	"encoding/json"
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
	"github.com/kaeawc/spectra/internal/process"
)

const networkTopUsage = "usage: spectra network top [--duration 10s] [--interval 1s] [--top 10] [--pid N] [--json]"

// networkTopDeps are the seams runNetworkTopWithIO needs; tests replace them.
type networkTopDeps struct {
	os    hostos.Kind
	run   netstate.CmdRunner
	procs func(ctx context.Context) []process.Info
	conns func() []netstate.Connection
	cwds  func(ctx context.Context, pids []int) map[int]string
	sleep func(ctx context.Context, d time.Duration) error
	clock clock.Clock
}

func defaultNetworkTopDeps() networkTopDeps {
	return networkTopDeps{
		run: netstate.DefaultRunner,
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
			"Show which processes are using the network right now: sample per-process\n"+
			"byte counters each interval (macOS nettop, Linux ss TCP sockets) and rank\n"+
			"processes by bytes moved over the window. Ctrl-C ends early.\n\n")
		fs.PrintDefaults()
	}
	fs.DurationVar(&o.duration, "duration", 10*time.Second, "How long to sample (Ctrl-C stops early)")
	fs.DurationVar(&o.interval, "interval", time.Second, "Sampling interval")
	fs.IntVar(&o.top, "top", 10, "Number of processes to list")
	fs.IntVar(&o.pid, "pid", 0, "Only this pid and its descendants")
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
	sample, basis, tool, err := nettop.SamplerFor(deps.os, deps.run)
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

	ew := &errWriter{w: stdout}
	cfg := nettop.Config{
		Sample: sample, Tool: tool, Sleep: deps.sleep, Clock: deps.clock,
		Interval: opts.interval, Duration: opts.duration,
		Preexisting: pidSet(before), EmitInclude: scope.include(),
	}
	if !opts.json {
		fmt.Fprintf(ew, "Sampling %s every %s for %s (Ctrl-C to stop early)\n", tool, opts.interval, opts.duration)
		cfg.Emit = func(t nettop.Tick) { printNetworkTopTick(ew, t) }
	}
	tr, err := nettop.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "network top: %v\n", err)
		return 1
	}

	// The window may have ended on Ctrl-C; enrichment still needs to run.
	post := context.WithoutCancel(ctx)
	after := deps.procs(post)
	scope.extend(after)
	rep := nettop.Summarize(tr, scope.include(), opts.top)
	rep.Basis, rep.PID = basis, opts.pid
	rep.Limitations = append(rep.Limitations, nettop.Limitations(basis)...)
	if opts.pid > 0 {
		rep.Limitations = append(rep.Limitations, "--pid: descendants are taken from process tables read before and after the window, so a child that started and exited inside it is not included")
	}
	enrichNetworkTop(post, &rep, deps, before, after)

	if opts.json {
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

// topScope is the --pid subtree: the target plus every descendant seen in
// any process table read.
type topScope struct {
	pid  int
	pids map[int]bool
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

func pidSet(procs []process.Info) map[int]bool {
	if len(procs) == 0 {
		return nil
	}
	set := make(map[int]bool, len(procs))
	for _, p := range procs {
		set[p.PID] = true
	}
	return set
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
	if missing > 0 {
		rep.Limitations = append(rep.Limitations, fmt.Sprintf("remote endpoints come from one connection listing at the end; %d listed process(es) had none open then", missing))
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
	if len(r.Processes) == 0 {
		fmt.Fprintln(w, "  no process moved any bytes in the window")
	} else {
		printNetworkTopTable(w, r.Processes)
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

func printNetworkTopTable(w io.Writer, ps []nettop.Process) {
	fmt.Fprintf(w, "\n  %7s  %-20s  %-10s  %9s  %9s  %11s  %11s  %11s  %11s  %s\n",
		"PID", "COMMAND", "USER", "IN", "OUT", "AVG IN", "AVG OUT", "PEAK IN", "PEAK OUT", "REMOTES")
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
