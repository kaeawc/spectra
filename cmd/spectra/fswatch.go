package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/fswatch"
	"github.com/kaeawc/spectra/internal/owner"
)

// fswatchDeps are the seams runFswatchWithIO needs; tests replace them.
type fswatchDeps struct {
	fs      fswatch.FS
	clock   clock.Clock
	sleep   func(ctx context.Context, d time.Duration) error
	holders func(ctx context.Context, path string) ([]fswatch.Holder, error)
}

func defaultFswatchDeps() fswatchDeps {
	return fswatchDeps{
		fs:      fswatch.OSFS{},
		clock:   clock.Default,
		sleep:   fswatch.RealSleep,
		holders: fswatchHolders,
	}
}

func fswatchHolders(ctx context.Context, path string) ([]fswatch.Holder, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	res, err := owner.Finder{}.Find(ctx, owner.Target{Kind: owner.KindPath, Raw: path, Path: abs})
	if err != nil {
		return nil, fmt.Errorf("owner lookup: %w", err)
	}
	out := make([]fswatch.Holder, 0, len(res.Holders))
	for _, h := range res.Holders {
		out = append(out, fswatch.Holder{PID: h.PID, Command: h.Command, Mode: strings.TrimSpace(h.Hold + " " + h.Mode)})
	}
	return out, nil
}

func runFswatch(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runFswatchWithIO(ctx, args, os.Stdout, os.Stderr, defaultFswatchDeps())
}

const fswatchUsage = "usage: spectra fswatch <path> [--duration 30s] [--interval 1s] [--top 10] [--max-entries N] [--json]"

func runFswatchWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, deps fswatchDeps) int {
	fs := flag.NewFlagSet("spectra fswatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "%s\n\nWatch a directory tree by polling and report what keeps changing.\nNote: %s.\n\n",
			fswatchUsage, strings.Join(fswatch.Limitations[:2], "; "))
		fs.PrintDefaults()
	}
	duration := fs.Duration("duration", 30*time.Second, "How long to watch (Ctrl-C stops early)")
	interval := fs.Duration("interval", time.Second, "Polling interval")
	top := fs.Int("top", 10, "Show the top N busiest files and directories")
	maxEntries := fs.Int("max-entries", fswatch.DefaultMaxEntries, "Maximum tracked entries before the tree is reported as truncated")
	asJSON := fs.Bool("json", false, "Emit one JSON object at the end instead of streaming text")

	// Accept flags before or after the single path argument.
	var paths []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		paths = append(paths, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(paths) != 1 || *duration <= 0 || *interval <= 0 || *top < 1 || *maxEntries < 1 {
		fmt.Fprintf(stderr, "fswatch: exactly one <path> is required; --duration, --interval, --top and --max-entries must be positive\n%s\n", fswatchUsage)
		return 2
	}
	if *interval > *duration {
		fmt.Fprintln(stderr, "fswatch: --interval must not exceed --duration")
		return 2
	}

	ew := &errWriter{w: stdout}
	cfg := fswatch.Config{
		Root: filepath.Clean(paths[0]), Interval: *interval, Duration: *duration,
		Top: *top, MaxEntries: *maxEntries,
		FS: deps.fs, Clock: deps.clock, Sleep: deps.sleep,
	}
	if !*asJSON {
		cfg.Emit = func(e fswatch.Event) { fmt.Fprintln(ew, formatFswatchEvent(e)) }
	}
	res, err := fswatch.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fswatch: %v\n", err)
		return 1
	}
	attributeHolders(ctx, &res, deps.holders)

	if *asJSON {
		enc := json.NewEncoder(ew)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	} else {
		printFswatchSummary(ew, res)
	}
	if ew.err != nil {
		fmt.Fprintf(stderr, "fswatch: write output: %v\n", ew.err)
		return 1
	}
	return 0
}

// attributeHolders names holders for busiest files that still exist. It is
// skipped after cancellation because the lookup would be cut off anyway.
func attributeHolders(ctx context.Context, res *fswatch.Result, lookup func(context.Context, string) ([]fswatch.Holder, error)) {
	if lookup == nil || ctx.Err() != nil {
		return
	}
	files := res.Summary.BusiestFiles
	for i := range files {
		if !files[i].Exists {
			continue
		}
		hs, err := lookup(ctx, files[i].Path)
		if err != nil {
			files[i].HolderError = err.Error()
			continue
		}
		files[i].Holders = hs
	}
}

func formatFswatchEvent(e fswatch.Event) string {
	kind := e.Kind
	if e.IsDir {
		kind += " dir"
	}
	return fmt.Sprintf("%s  %-12s %10d B  %s", e.Time.Format("15:04:05.000"), kind, e.Size, e.Path)
}

func printFswatchSummary(w io.Writer, r fswatch.Result) {
	s := r.Summary
	fmt.Fprintf(w, "\nSummary: %d events (%d created, %d modified, %d removed) under %s over %dms (%d polls, interval %dms, %d entries tracked)\n",
		s.TotalEvents, s.Counts.Created, s.Counts.Modified, s.Counts.Removed, r.Root, r.DurationMS, r.Polls, r.IntervalMS, s.TrackedEntries)
	if s.TruncationNote != "" {
		fmt.Fprintf(w, "TRUNCATED: %s\n", s.TruncationNote)
	}
	if s.EventsDropped > 0 {
		fmt.Fprintf(w, "%d events were counted but not retained in the event list\n", s.EventsDropped)
	}
	if len(s.BusiestFiles) > 0 {
		fmt.Fprintf(w, "Busiest files (top %d of %d):\n", len(s.BusiestFiles), s.DistinctFiles)
		for _, f := range s.BusiestFiles {
			fmt.Fprintf(w, "  %5d  %s\n", f.Events, f.Path)
			for _, h := range f.Holders {
				fmt.Fprintf(w, "         held by pid %d %s (%s)\n", h.PID, h.Command, h.Mode)
			}
			if f.HolderError != "" {
				fmt.Fprintf(w, "         holders unavailable: %s\n", f.HolderError)
			}
		}
	}
	if len(s.BusiestDirs) > 0 {
		fmt.Fprintf(w, "Busiest directories (top %d of %d):\n", len(s.BusiestDirs), s.DistinctDirs)
		for _, d := range s.BusiestDirs {
			fmt.Fprintf(w, "  %5d  %s\n", d.Events, d.Path)
		}
	}
	fmt.Fprintln(w, "Limitations:")
	for _, l := range r.Limits {
		fmt.Fprintf(w, "  - %s\n", l)
	}
}
