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
	"regexp"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
)

func runLogs(args []string) int {
	return runLogsWithIO(args, os.Stdout, os.Stderr, logquery.Query{})
}

// runLogsWithIO runs the query the flags describe on top of base, which
// supplies the backend seams (OS, Runner, Now) in tests.
func runLogsWithIO(args []string, stdout, stderr io.Writer, base logquery.Query) int {
	fs := flag.NewFlagSet("spectra logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	processName := fs.String("process", "", "Filter by process")
	subsystem := fs.String("subsystem", "", "Filter by subsystem")
	level := fs.String("level", "", "Minimum level: Default, Info, Error, Fault")
	last := fs.Duration("last", time.Hour, "Lookback duration")
	grep := fs.String("grep", "", "Filter messages by regexp after query")
	top := fs.Int("top", 5000, "Maximum rows")
	asJSON := fs.Bool("json", false, "Emit JSON")
	stream := fs.Bool("stream", false, "Stream live logs")
	predicate := fs.String("predicate", "", "Raw NSPredicate")
	unsafePredicate := fs.Bool("unsafe-predicate", false, "Allow raw NSPredicate")
	allowLongWindow := fs.Bool("allow-long-window", false, "Allow windows over 24h")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	q := base
	q.Process, q.Subsystem, q.MinLevel = *processName, *subsystem, *level
	q.Last, q.MaxRows = *last, *top
	q.Predicate, q.AllowUnsafePredicate, q.AllowLongWindow = *predicate, *unsafePredicate, *allowLongWindow
	if *stream {
		return runLogStream(q, *grep, *asJSON)
	}
	var re *regexp.Regexp
	if *grep != "" {
		compiled, err := regexp.Compile(*grep)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		re = compiled
	}
	result, err := logquery.Report(context.Background(), q, re)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if errors.Is(err, logquery.ErrWindowTooLarge) || errors.Is(err, logquery.ErrUnsafePredicate) {
			return 2
		}
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printLogs(stdout, result)
	return 0
}

func runLogStream(q logquery.Query, grep string, asJSON bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	entries, errs, err := logquery.Stream(ctx, q)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var re *regexp.Regexp
	if grep != "" {
		compiled, err := regexp.Compile(grep)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		re = compiled
	}
	enc := json.NewEncoder(os.Stdout)
	for entry := range entries {
		if re != nil && !re.MatchString(entry.EventMessage) && !re.MatchString(entry.FormatString) {
			continue
		}
		if asJSON {
			_ = enc.Encode(entry)
			continue
		}
		printLogEntry(os.Stdout, entry)
	}
	for err := range errs {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func printLogs(w io.Writer, result logquery.Result) {
	fmt.Fprintf(w, "entries: %d", len(result.Entries))
	if result.Truncated {
		fmt.Fprint(w, " (truncated)")
	}
	if result.Clock.WallClockAdjusted {
		fmt.Fprint(w, " wall-clock-adjusted")
	}
	fmt.Fprintln(w)
	for _, entry := range result.Entries {
		printLogEntry(w, entry)
	}
}

func printLogEntry(w io.Writer, entry logquery.LogEntry) {
	fmt.Fprintf(w, "%s  pid=%d  %-7s  %-18s  %s\n",
		entry.Timestamp.Format(time.RFC3339),
		entry.PID,
		entry.LogType,
		truncate(entry.Process, 18),
		entry.EventMessage,
	)
}
