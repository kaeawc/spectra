package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/storagegrowth"
	"github.com/kaeawc/spectra/internal/storagestate"
)

// snapshotStorage is the subset of a snapshot JSON the growth report needs.
type snapshotStorage struct {
	TakenAt time.Time          `json:"taken_at"`
	Storage storagestate.State `json:"storage"`
}

func runStorageGrowth(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spectra storage growth", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", 10, "Show the top N growing areas (0 for all)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *top < 0 {
		fmt.Fprintln(stderr, "storage growth: --top must be >= 0")
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: spectra storage growth [--top N] [--json] <before-snapshot.json> <after-snapshot.json>")
		return 2
	}

	before, err := readSnapshotStorage(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "storage growth: %v\n", err)
		return 1
	}
	after, err := readSnapshotStorage(fs.Arg(1))
	if err != nil {
		fmt.Fprintf(stderr, "storage growth: %v\n", err)
		return 1
	}

	rep := storagegrowth.Compute(before.Storage, after.Storage, before.TakenAt, after.TakenAt, *top)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "storage growth: write output: %v\n", err)
			return 1
		}
		return 0
	}
	printStorageGrowth(stdout, rep)
	return 0
}

func readSnapshotStorage(path string) (snapshotStorage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshotStorage{}, fmt.Errorf("read %s: %w", path, err)
	}
	var s snapshotStorage
	if err := json.Unmarshal(data, &s); err != nil {
		return snapshotStorage{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.TakenAt.IsZero() {
		return snapshotStorage{}, fmt.Errorf("%s: no taken_at timestamp (not a snapshot?)", path)
	}
	return s, nil
}

func printStorageGrowth(w io.Writer, rep storagegrowth.Report) {
	fmt.Fprintf(w, "=== storage growth (%s → %s, %.1f days) ===\n",
		rep.BeforeAt.Local().Format("2006-01-02 15:04"),
		rep.AfterAt.Local().Format("2006-01-02 15:04"), rep.Days)
	fmt.Fprintf(w, "total growth: %s\n", humanSize(rep.TotalGrowthBytes))
	if rep.Note != "" {
		fmt.Fprintf(w, "note: %s\n", rep.Note)
	}
	if len(rep.Deltas) == 0 {
		fmt.Fprintln(w, "  (nothing grew)")
		return
	}
	fmt.Fprintf(w, "  %12s  %12s  %-10s  %s\n", "GROWTH", "PER DAY", "CATEGORY", "AREA")
	for _, d := range rep.Deltas {
		fmt.Fprintf(w, "  %12s  %12s  %-10s  %s\n",
			humanSize(d.GrowthBytes), humanSize(int64(d.BytesPerDay))+"/d",
			truncate(d.Category, 10), truncate(d.Label, 42))
	}
}
