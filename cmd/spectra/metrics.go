package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

func runMetrics(args []string) int {
	fs := flag.NewFlagSet("spectra metrics", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	limit := fs.Int("n", 60, "Number of 1-minute rows to show (stored history)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	dbPath, err := store.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()

	if fs.NArg() == 0 {
		// Summary: list all PIDs that have stored metrics.
		return printMetricsSummary(ctx, db, *limit, *asJSON)
	}

	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	return printMetricsForPID(ctx, db, pid, *limit, *asJSON)
}

func printMetricsSummary(ctx context.Context, db *store.DB, limit int, asJSON bool) int {
	rows, err := db.GetAllProcessMetrics(ctx, limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no metrics stored — start `spectra serve` to collect process metrics")
		return 0
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	// Group by PID, show the most recent row per PID.
	byPID := make(map[int]store.ProcessMetricRow)
	for _, r := range rows {
		if _, seen := byPID[r.PID]; !seen {
			byPID[r.PID] = r
		}
	}
	pids := make([]int, 0, len(byPID))
	for pid := range byPID {
		pids = append(pids, pid)
	}
	sort.Ints(pids)

	fmt.Printf("%-7s  %-12s  %-12s  %-8s  %s\n", "PID", "AVG_RSS_MiB", "MAX_RSS_MiB", "AVG_CPU%", "MINUTE")
	fmt.Println(strings.Repeat("-", 65))
	for _, pid := range pids {
		r := byPID[pid]
		fmt.Printf("%-7d  %-12s  %-12s  %-8.1f  %s\n",
			r.PID,
			fmt.Sprintf("%.1f", float64(r.AvgRSSKiB)/1024),
			fmt.Sprintf("%.1f", float64(r.MaxRSSKiB)/1024),
			r.AvgCPUPct,
			r.MinuteAt.UTC().Format(time.RFC3339),
		)
	}
	return 0
}

func printMetricsForPID(ctx context.Context, db *store.DB, pid, limit int, asJSON bool) int {
	rows, err := db.GetProcessMetrics(ctx, pid, limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "no metrics for PID %d\n", pid)
		return 0
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	fmt.Printf("metrics for PID %d (newest first)\n", pid)
	fmt.Printf("%-20s  %-12s  %-12s  %-8s  %s\n", "MINUTE", "AVG_RSS_MiB", "MAX_RSS_MiB", "AVG_CPU%", "SAMPLES")
	fmt.Println(strings.Repeat("-", 72))
	for _, r := range rows {
		fmt.Printf("%-20s  %-12s  %-12s  %-8.1f  %d\n",
			r.MinuteAt.UTC().Format("2006-01-02T15:04Z"),
			fmt.Sprintf("%.1f", float64(r.AvgRSSKiB)/1024),
			fmt.Sprintf("%.1f", float64(r.MaxRSSKiB)/1024),
			r.AvgCPUPct,
			r.SampleCount,
		)
	}
	return 0
}
