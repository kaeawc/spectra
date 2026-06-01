package main

import (
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
	since := fs.Duration("since", 30*24*time.Hour, "Look back duration")
	grep := fs.String("grep", "", "Substring filter on message")
	asJSON := fs.Bool("json", false, "Emit JSON")
	maxLines := fs.Int("max-lines", 5000, "Maximum entries")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	result, err := updates.QueryInstallLog(updates.Query{
		Since:    time.Now().Add(-*since),
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
