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
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/memtrace"
	"github.com/kaeawc/spectra/internal/process"
)

// memtraceDeps are the seams runMemtraceWithIO needs; tests replace them.
type memtraceDeps struct {
	collect func(ctx context.Context) []process.Info
	sleep   func(ctx context.Context, d time.Duration) error
	clock   clock.Clock
}

func defaultMemtraceDeps() memtraceDeps {
	return memtraceDeps{
		collect: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{
				DetailCollector: func([]process.Info) map[int]process.Details { return nil },
				MemoryBytes:     func() uint64 { return 0 },
			})
		},
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

func runMemtrace(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runMemtraceWithIO(ctx, args, os.Stdout, os.Stderr, defaultMemtraceDeps())
}

func runMemtraceWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, deps memtraceDeps) int {
	fs := flag.NewFlagSet("spectra memtrace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, "usage: spectra memtrace <pid> [--tree] [--duration 60s] [--interval 2s] [--json]\n\n"+
			"Sample a process's resident memory over time and say whether it is growing.\n\n"+
			"The growth rate is the Theil-Sen slope (median of pairwise slopes), so one\n"+
			"spike or where the first and last samples land does not move it. The verdict\n"+
			"compares slope x span with max(5% of median RSS, 1 MiB): growing, shrinking\n"+
			"or steady. Two or more full cycles with swings of 10% of the median are a\n"+
			"sawtooth, judged by its troughs (\"growing under a sawtooth\" when they rise).\n"+
			"unreadable means every reading was 0; insufficient means fewer than 5\n"+
			"samples, so --duration must be at least 4 x --interval.\n\n")
		fs.PrintDefaults()
	}
	tree := fs.Bool("tree", false, "Also sum and track the whole descendant subtree")
	duration := fs.Duration("duration", 60*time.Second, "How long to sample (Ctrl-C stops early)")
	interval := fs.Duration("interval", 2*time.Second, "Sampling interval")
	asJSON := fs.Bool("json", false, "Emit one JSON object")
	pidArg, ok := parseMemtraceArgs(fs, args)
	if !ok {
		return 2
	}
	pid, err := strconv.Atoi(pidArg)
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "memtrace: invalid pid %q\n", pidArg)
		return 2
	}
	if *interval <= 0 || *duration <= 0 {
		fmt.Fprintln(stderr, "memtrace: --interval and --duration must be positive")
		return 2
	}

	if n := memtrace.MaxSamples(*duration, *interval); n < memtrace.MinSamples {
		fmt.Fprintf(stderr, "memtrace: hint: --duration %s at --interval %s gives at most %d samples; a verdict needs %d (duration >= %d intervals)\n",
			*duration, *interval, n, memtrace.MinSamples, memtrace.MinSamples-1)
	}

	ew := &errWriter{w: stdout}
	cfg := memtrace.Config{
		Collect: deps.collect, Sleep: deps.sleep, Clock: deps.clock,
		PID: pid, Tree: *tree, Interval: *interval, Duration: *duration,
	}
	if !*asJSON {
		cfg.Emit = func(s memtrace.Sample) { printMemtraceSample(ew, s, *tree) }
	}
	res, err := memtrace.Run(ctx, cfg)
	if err != nil {
		if errors.Is(err, memtrace.ErrNotFound) {
			fmt.Fprintf(stderr, "memtrace: pid %d not found in the process table\n", pid)
		} else {
			fmt.Fprintf(stderr, "memtrace: %v\n", err)
		}
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(ew)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res) // failures latch in ew
	} else {
		_ = printMemtraceSummary(ew, res) // failures latch in ew
	}
	if ew.err != nil {
		fmt.Fprintf(stderr, "memtrace: write output: %v\n", ew.err)
		return 1
	}
	return 0
}

// parseMemtraceArgs accepts the pid before or after the flags.
func parseMemtraceArgs(fs *flag.FlagSet, args []string) (string, bool) {
	if err := fs.Parse(args); err != nil {
		return "", false
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(fs.Output(), "usage: spectra memtrace <pid> [--tree] [--duration 60s] [--interval 2s] [--json]")
		return "", false
	}
	pid := rest[0]
	if err := fs.Parse(rest[1:]); err != nil {
		return "", false
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(fs.Output(), "memtrace: exactly one pid is accepted")
		return "", false
	}
	return pid, true
}

func printMemtraceSample(w io.Writer, s memtrace.Sample, tree bool) {
	fmt.Fprintf(w, "%7.1fs  rss %10d KiB", float64(s.ElapsedMS)/1000, s.RSSKiB)
	if tree {
		fmt.Fprintf(w, "  subtree %10d KiB  %d processes", s.SubtreeRSSKiB, s.Processes)
	}
	fmt.Fprintln(w)
}

func printMemtraceSummary(out io.Writer, r *memtrace.Result) error {
	w := &errWriter{w: out}
	s := r.Summary
	fmt.Fprintf(w, "\nSummary for %s (pid %d) over %.1fs\n", r.Command, r.PID, float64(r.DurationMS)/1000)
	if s.Exited {
		fmt.Fprintln(w, "Process exited during the trace; showing what was collected.")
	}
	fmt.Fprintf(w, "  samples: %d\n  min: %d KiB  max: %d KiB\n  first: %d KiB  last: %d KiB\n",
		s.Samples, s.MinKiB, s.MaxKiB, s.FirstKiB, s.LastKiB)
	fmt.Fprintf(w, "  net change: %+d KiB\n  growth rate: %+.1f KiB/min\n  verdict: %s\n",
		s.NetChangeKiB, s.GrowthKiBPerMin, memtraceVerdictText(s.Verdict, s.Pattern))
	if s.Verdict == memtrace.VerdictInsufficient {
		fmt.Fprintf(w, "  (need at least %d samples for a verdict)\n", memtrace.MinSamples)
	}
	if r.PollsFailed > 0 || r.PollsSkipped > 0 {
		fmt.Fprintf(w, "  polls: %d failed, %d skipped (a collection overran its deadline)\n", r.PollsFailed, r.PollsSkipped)
	}
	if r.Tree && s.Samples > 0 {
		fmt.Fprintf(w, "  subtree: %d KiB -> %d KiB (%+.1f KiB/min, %s)\n", s.SubtreeFirstKiB, s.SubtreeLastKiB,
			s.SubtreeGrowthMin, memtraceVerdictText(s.SubtreeVerdict, s.SubtreePattern))
	}
	if len(r.Growers) > 0 {
		fmt.Fprintln(w, "Top descendants by RSS growth:")
		for _, g := range r.Growers {
			fmt.Fprintf(w, "  %6d  %-20s  %d -> %d KiB (%+d)\n", g.PID, g.Command, g.FirstKiB, g.LastKiB, g.GrowKiB)
		}
	}
	return w.err
}

func memtraceVerdictText(verdict, pattern string) string {
	switch {
	case verdict == memtrace.VerdictUnreadable:
		return verdict + " (no memory readings)"
	case pattern == memtrace.PatternSawtooth:
		return verdict + " under a sawtooth"
	}
	return verdict
}
