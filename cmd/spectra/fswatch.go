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
	"github.com/kaeawc/spectra/internal/diskusage"
	"github.com/kaeawc/spectra/internal/fswatch"
	"github.com/kaeawc/spectra/internal/owner"
)

// fswatchDeps are the seams runFswatchWithIO needs; tests replace them.
type fswatchDeps struct {
	fs      fswatch.FS
	clock   clock.Clock
	sleep   func(ctx context.Context, d time.Duration) error
	holders func(ctx context.Context, path string) (hs []fswatch.Holder, incomplete bool, err error)
}

func defaultFswatchDeps() fswatchDeps {
	return fswatchDeps{
		fs:      fswatch.OSFS{},
		clock:   clock.Default,
		sleep:   fswatch.RealSleep,
		holders: fswatchHolders,
	}
}

func fswatchHolders(ctx context.Context, path string) ([]fswatch.Holder, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, fmt.Errorf("resolve %s: %w", path, err)
	}
	res, err := owner.Finder{}.Find(ctx, owner.Target{Kind: owner.KindPath, Raw: path, Path: abs})
	if err != nil {
		return nil, false, fmt.Errorf("owner lookup: %w", err)
	}
	out := make([]fswatch.Holder, 0, len(res.Holders))
	for _, h := range res.Holders {
		out = append(out, fswatch.Holder{PID: h.PID, Command: h.Command, Mode: strings.TrimSpace(h.Hold + " " + h.Mode)})
	}
	return out, res.Incomplete, nil
}

func runFswatch(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// Restore default handling after the first interrupt so a second Ctrl-C
	// terminates the process even while a scan or lookup is running.
	go func() {
		<-ctx.Done()
		stop()
	}()
	return runFswatchWithIO(ctx, args, os.Stdout, os.Stderr, defaultFswatchDeps())
}

const fswatchUsage = "usage: spectra fswatch <path> [--duration 30s] [--interval 1s] [--top 10] [--max-entries N] [--holders-timeout 10s] [--json]"

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
	holdersTimeout := fs.Duration("holders-timeout", 10*time.Second, "Total time allowed for naming holders of the busiest files (0 disables holder lookup)")
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
	if len(paths) != 1 || *duration <= 0 || *interval <= 0 || *top < 1 || *maxEntries < 1 || *holdersTimeout < 0 {
		fmt.Fprintf(stderr, "fswatch: exactly one <path> is required; --duration, --interval, --top and --max-entries must be positive and --holders-timeout must not be negative\n%s\n", fswatchUsage)
		return 2
	}
	if *interval > *duration {
		fmt.Fprintln(stderr, "fswatch: --interval must not exceed --duration")
		return 2
	}

	ew := &errWriter{w: stdout}
	cfg := fswatch.Config{
		Root: fswatchRoot(paths[0]), Interval: *interval, Duration: *duration,
		Top: *top, MaxEntries: *maxEntries,
		FS: deps.fs, Clock: deps.clock, Sleep: deps.sleep,
	}
	if !*asJSON {
		cfg.Emit = func(e fswatch.Event) { fmt.Fprintln(ew, formatFswatchEvent(e)) }
		cfg.Warn = func(m string) { fmt.Fprintf(ew, "warning: %s\n", m) }
	}
	res, err := fswatch.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "fswatch: %v\n", err)
		return 1
	}
	if *holdersTimeout > 0 {
		attributeHolders(ctx, &res, deps.holders, *holdersTimeout)
	}

	writeFswatchResult(ew, res, *asJSON)
	if ew.err != nil {
		fmt.Fprintf(stderr, "fswatch: write output: %v\n", ew.err)
		return 1
	}
	return 0
}

// fswatchRoot returns the path to watch. The lexically cleaned argument is
// kept unless it would traverse somewhere other than the real target (for
// example "link/.." where link is a symlink), in which case the properly
// resolved root is used.
func fswatchRoot(arg string) string {
	clean := filepath.Clean(arg)
	resolved, err := diskusage.ResolveRoot(arg)
	if err != nil {
		return clean
	}
	if lex, err := filepath.EvalSymlinks(clean); err == nil && lex == resolved {
		return clean
	}
	return resolved
}

func writeFswatchResult(w io.Writer, res fswatch.Result, asJSON bool) {
	if !asJSON {
		printFswatchSummary(w, res)
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(res) // write errors are latched by the errWriter
}

// maxHolderFiles caps how many busiest files get a holder lookup; each one
// costs a full owner scan.
const maxHolderFiles = 5

// attributeHolders names holders for the busiest files that are still regular
// files, within one overall timeout. Anything short of a complete answer sets
// HoldersIncomplete so a missing holder is never read as "nobody has it open".
func attributeHolders(ctx context.Context, res *fswatch.Result, lookup func(context.Context, string) ([]fswatch.Holder, bool, error), timeout time.Duration) {
	if lookup == nil {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	files := res.Summary.BusiestFiles
	for i := range files {
		if !files[i].Exists || i >= maxHolderFiles {
			continue
		}
		if pctx.Err() != nil {
			res.Summary.HoldersIncomplete = true
			files[i].HolderError = "holder lookup skipped: " + pctx.Err().Error()
			continue
		}
		r, ok := runHolderLookup(pctx, lookup, files[i].Path)
		if !ok {
			// The lookup ignored the context; stop waiting and skip the rest.
			res.Summary.HoldersIncomplete = true
			files[i].HolderError = "holder lookup timed out: " + pctx.Err().Error()
			continue
		}
		if r.incomplete {
			res.Summary.HoldersIncomplete = true
		}
		if r.err != nil {
			res.Summary.HoldersIncomplete = true
			files[i].HolderError = r.err.Error()
			continue
		}
		files[i].Holders = r.hs
	}
}

type holderResult struct {
	hs         []fswatch.Holder
	incomplete bool
	err        error
}

// runHolderLookup waits for lookup or ctx, whichever ends first. A lookup that
// ignores ctx is abandoned and ends with the process; the buffered channel
// keeps it from blocking forever on send.
func runHolderLookup(ctx context.Context, lookup func(context.Context, string) ([]fswatch.Holder, bool, error), path string) (holderResult, bool) {
	ch := make(chan holderResult, 1)
	go func() {
		hs, incomplete, err := lookup(ctx, path)
		ch <- holderResult{hs, incomplete, err}
	}()
	select {
	case r := <-ch:
		return r, true
	case <-ctx.Done():
		return holderResult{}, false
	}
}

func formatFswatchEvent(e fswatch.Event) string {
	return fmt.Sprintf("%s  %-8s %-7s %10d B  %s", e.Time.Format("15:04:05.000"), e.Kind, e.EntryKind, e.Size, e.Path)
}

func printFswatchSummary(w io.Writer, r fswatch.Result) {
	s := r.Summary
	fmt.Fprintf(w, "\nSummary: %d events (%d created, %d modified, %d removed) under %s over %dms (%d polls, interval %dms, %d entries tracked)\n",
		s.TotalEvents, s.Counts.Created, s.Counts.Modified, s.Counts.Removed, r.Root, r.DurationMS, r.Polls, r.IntervalMS, s.TrackedEntries)
	if r.EndedReason != "" {
		fmt.Fprintf(w, "WATCH ENDED EARLY: %s\n", r.EndedReason)
	}
	if r.PollsFailed > 0 {
		fmt.Fprintf(w, "%d polls failed to read the root and were skipped\n", r.PollsFailed)
	}
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
	if s.HoldersIncomplete {
		fmt.Fprintln(w, "Holders may be incomplete (not root, lookup timed out or was skipped); an empty list does not mean nobody has the file open")
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
