package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/updates"
)

func runUpdates(args []string) int {
	fs := flag.NewFlagSet("spectra updates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	history := fs.Bool("history", false, "Show system_profiler install history")
	since := fs.Duration("since", 0, "Look back duration (default: 30d for logs, all history)")
	source := fs.String("source", "", "History source filter: apple or third-party")
	grep := fs.String("grep", "", "Regex filter for history; substring filter for logs")
	asJSON := fs.Bool("json", false, "Emit JSON")
	maxLines := fs.Int("max-lines", 5000, "Maximum entries")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *history {
		return runUpdatesHistory(*since, *source, *grep, *asJSON)
	}
	logSince := time.Now().Add(-30 * 24 * time.Hour)
	if *since > 0 {
		logSince = time.Now().Add(-*since)
	}
	result, err := updates.QueryInstallLog(updates.Query{
		Since:    logSince,
		Grep:     *grep,
		MaxLines: *maxLines,
	})
	if err != nil {
		if errors.Is(err, updates.ErrNeedsFullDiskAccess) {
			fmt.Fprintln(os.Stderr, updates.FullDiskAccessRemediation)
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printUpdates(result)
	return 0
}

func runUpdatesHistory(since time.Duration, source string, grep string, asJSON bool) int {
	history, err := updates.Collect(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	query := updates.HistoryQuery{Source: source, Grep: grep}
	if since > 0 {
		query.Since = time.Now().Add(-since)
	}
	filtered, err := updates.FilterHistory(history, query)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(filtered)
		return 0
	}
	printUpdateHistory(filtered)
	return 0
}

func printUpdates(result updates.Result) {
	fmt.Printf("entries: %d\n", len(result.Entries))
	for _, entry := range result.Entries {
		fmt.Printf("%s  %-18s  %s\n",
			entry.Timestamp.Format(time.RFC3339),
			entry.Process,
			entry.Message,
		)
	}
}

func printUpdateHistory(history updates.InstallHistory) {
	fmt.Printf("entries: %d\n", len(history.Entries))
	for _, entry := range history.Entries {
		fmt.Printf("%s  %-10s  %-36s  %s\n",
			entry.InstallDate.Format("2006-01-02 15:04:05"),
			entry.Source,
			truncate(entry.Name, 36),
			entry.Version,
		)
	}
}
