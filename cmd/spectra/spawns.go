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
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/spawntrace"
)

// spawnsDeps are the seams runSpawnsWithIO needs; tests replace them.
type spawnsDeps = spawntrace.Deps

func defaultSpawnsDeps() spawnsDeps { return spawntrace.DefaultDeps() }

func runSpawns(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runSpawnsWithIO(ctx, args, os.Stdout, os.Stderr, defaultSpawnsDeps())
}

func runSpawnsWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, deps spawnsDeps) int {
	fs := flag.NewFlagSet("spectra spawns", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: spectra spawns [--interval 250ms] [--duration 10s] [--pid N] [--json]\n\n"+
			"Trace process starts and exits live by polling the process table.\n"+
			"Note: %s. Lifetimes are approximate (start times have 1s resolution).\n"+
			"--interval must not exceed --duration. With --pid, an unknown pid is an error;\n"+
			"if that process exits mid-trace its tracked descendants are still followed.\n\n", spawntrace.PollingNote)
		fs.PrintDefaults()
	}
	def := spawntrace.DefaultOptions()
	interval := fs.Duration("interval", def.Interval, "Polling interval (processes shorter than this can be missed)")
	duration := fs.Duration("duration", def.Duration, "How long to trace (Ctrl-C stops early)")
	pid := fs.Int("pid", 0, "Only trace this process and its descendants")
	asJSON := fs.Bool("json", false, "Emit one JSON object per event line and a final summary object")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := spawntrace.Options{Interval: *interval, Duration: *duration, PID: *pid}
	verr := opts.Validate()
	if fs.NArg() != 0 || errors.Is(verr, spawntrace.ErrInvalidOptions) {
		fmt.Fprintln(stderr, "spawns: --interval and --duration must be positive, --pid must not be negative, and no arguments are accepted")
		return 2
	}
	if verr != nil {
		fmt.Fprintln(stderr, "spawns: --interval must not exceed --duration")
		return 2
	}

	var werr error
	write := func(format string, a ...any) {
		if werr == nil {
			_, werr = fmt.Fprintf(stdout, format, a...)
		}
	}
	emit := func(e spawntrace.Event) {
		if *asJSON {
			b, err := json.Marshal(e)
			if err != nil {
				werr = err
				return
			}
			write("%s\n", b)
			return
		}
		write("%s\n", formatSpawnEvent(e))
	}

	notify := func(msg string) {
		if *asJSON {
			fmt.Fprintf(stderr, "spawns: %s\n", msg)
			return
		}
		write("NOTE: %s\n", msg)
	}
	sum := spawntrace.Run(ctx, opts.Config(deps, emit, notify))

	if sum.RootNotFound {
		fmt.Fprintf(stderr, "spawns: pid %d not found in the process table\n", *pid)
		return 1
	}

	if *asJSON {
		b, err := json.Marshal(sum)
		if err != nil {
			werr = err
		} else {
			write("%s\n", b)
		}
	} else {
		write("%s", formatSpawnSummary(sum))
	}
	if werr != nil {
		fmt.Fprintf(stderr, "spawns: write output: %v\n", werr)
		return 1
	}
	return 0
}

func formatSpawnEvent(e spawntrace.Event) string {
	tag := "START"
	extra := ""
	if e.Kind == spawntrace.KindExit {
		tag = "EXIT "
		extra = fmt.Sprintf("  lived ~%.1fs", e.Lifetime.Seconds())
	}
	chain := "-"
	if len(e.Chain) > 0 {
		chain = strings.Join(e.Chain, " < ")
	}
	return fmt.Sprintf("%s %s pid=%d ppid=%d %s  (parents: %s)%s",
		e.Time.Format("15:04:05.000"), tag, e.PID, e.PPID, e.Command, chain, extra)
}

func cutTitle(title string, shown, total int) string {
	if total > shown {
		return fmt.Sprintf("%s (top %d of %d distinct)", title, shown, total)
	}
	return title
}

func formatSpawnSummary(s spawntrace.Summary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nSummary: %d started, %d exited over %s (%d polls, effective interval %s, %d ticks skipped)\n",
		s.Starts, s.Exits, s.Duration, s.Polls, s.EffectiveInterval.Round(time.Millisecond), s.SkippedTicks)
	fmt.Fprintf(&b, "Note: %s.\n", s.Note)
	section := func(title string, counts []spawntrace.Count) {
		if len(counts) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, c := range counts {
			fmt.Fprintf(&b, "  %4d  %s\n", c.Count, c.Name)
		}
	}
	if s.RootExited {
		b.WriteString("Scoped root exited during the trace; its tracked descendants were still followed.\n")
	}
	section(cutTitle("Spawns per parent", len(s.ByParent), s.ByParentTotal), s.ByParent)
	section(cutTitle("Spawns per command", len(s.ByCommand), s.ByCommandTotal), s.ByCommand)
	if len(s.Shortest) > 0 {
		b.WriteString("Shortest-lived:\n")
		for _, l := range s.Shortest {
			fmt.Fprintf(&b, "  %10s  %s (pid %d)\n", l.Lifetime.Round(time.Millisecond), l.Command, l.PID)
		}
	}
	if len(s.Loops) > 0 {
		fmt.Fprintf(&b, "Respawn loops (>= %d starts of one command under one parent):\n", spawntrace.LoopThreshold)
		for _, l := range s.Loops {
			fmt.Fprintf(&b, "  %4dx  %s under %s\n", l.Count, l.Command, l.Parent)
		}
	}
	return b.String()
}
