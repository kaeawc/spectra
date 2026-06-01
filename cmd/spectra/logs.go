package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
)

func runLogs(args []string) int {
	fs := flag.NewFlagSet("spectra logs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	processName := fs.String("process", "", "Filter by process")
	subsystem := fs.String("subsystem", "", "Filter by subsystem")
	level := fs.String("level", "", "Minimum level: Default, Info, Error, Fault")
	last := fs.Duration("last", time.Hour, "Lookback duration")
	grep := fs.String("grep", "", "Filter messages by regexp after query")
	top := fs.Int("top", 5000, "Maximum rows")
	asJSON := fs.Bool("json", false, "Emit JSON")
	predicate := fs.String("predicate", "", "Raw NSPredicate")
	unsafePredicate := fs.Bool("unsafe-predicate", false, "Allow raw NSPredicate")
	allowLongWindow := fs.Bool("allow-long-window", false, "Allow windows over 24h")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	result, err := logquery.Run(context.Background(), logquery.Query{
		Process:              *processName,
		Subsystem:            *subsystem,
		MinLevel:             *level,
		Last:                 *last,
		MaxRows:              *top,
		Predicate:            *predicate,
		AllowUnsafePredicate: *unsafePredicate,
		AllowLongWindow:      *allowLongWindow,
	})
	if err != nil {
		if errors.Is(err, logquery.ErrWindowTooLarge) || errors.Is(err, logquery.ErrUnsafePredicate) {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *grep != "" {
		filtered, err := grepLogEntries(result.Entries, *grep)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		result.Entries = filtered
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printLogs(result)
	return 0
}

func grepLogEntries(entries []logquery.LogEntry, expr string) ([]logquery.LogEntry, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	out := make([]logquery.LogEntry, 0, len(entries))
	for _, entry := range entries {
		if re.MatchString(entry.EventMessage) || re.MatchString(entry.FormatString) {
			out = append(out, entry)
		}
	}
	return out, nil
}

func printLogs(result logquery.Result) {
	fmt.Printf("entries: %d", len(result.Entries))
	if result.Truncated {
		fmt.Print(" (truncated)")
	}
	if result.Clock.WallClockAdjusted {
		fmt.Print(" wall-clock-adjusted")
	}
	fmt.Println()
	for _, entry := range result.Entries {
		fmt.Printf("%s  pid=%d  %-7s  %-18s  %s\n",
			entry.Timestamp.Format(time.RFC3339),
			entry.PID,
			entry.LogType,
			truncate(entry.Process, 18),
			entry.EventMessage,
		)
	}
}
