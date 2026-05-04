package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

// snapshotSubcommands returns snapshot sub-subcommands. Separate function
// to mirror the top-level pattern and avoid init cycles.
func snapshotSubcommands() []subcommand {
	return []subcommand{
		{"list", "List stored snapshots", runSnapshotList},
		{"show", "Show details of one snapshot by ID", runSnapshotShow},
	}
}

func runSnapshot(args []string) int {
	// Check for sub-subcommands first: snapshot list, snapshot show.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		for _, sc := range snapshotSubcommands() {
			if args[0] == sc.name {
				return sc.run(args[1:])
			}
		}
	}

	fs := flag.NewFlagSet("spectra snapshot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	withNetwork := fs.Bool("network", false, "Extract embedded URL hosts (slower; scans app.asar)")
	skipApps := fs.Bool("no-apps", false, "Skip the apps inventory; capture host info only")
	noStore := fs.Bool("no-store", false, "Do not persist the snapshot to the local database")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := snapshot.Options{
		SpectraVersion: version,
		DetectOpts:     detect.Options{ScanNetwork: *withNetwork},
	}
	if *skipApps {
		// Sentinel: a path that won't exist so detect drops it.
		opts.AppPaths = []string{"/dev/null/__skip_apps_marker__"}
	}
	snap := snapshot.Build(context.Background(), opts)
	if *skipApps {
		snap.Apps = nil
	}

	if !*noStore {
		if err := saveSnapshot(snap); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist snapshot: %v\n", err)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(snap)
		return 0
	}
	printSnapshot(snap)
	return 0
}

// saveSnapshot opens the default DB and persists snap.
func saveSnapshot(snap snapshot.Snapshot) error {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return db.SaveSnapshot(context.Background(), store.FromSnapshot(snap))
}

// runSnapshotList prints stored snapshots in a summary table.
func runSnapshotList(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
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

	rows, err := db.ListSnapshots(context.Background(), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "no snapshots stored — run `spectra snapshot` first")
		return 0
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	fmt.Printf("%-32s  %-8s  %-20s  %s\n", "ID", "KIND", "TAKEN AT", "APPS")
	fmt.Println(strings.Repeat("-", 72))
	for _, r := range rows {
		fmt.Printf("%-32s  %-8s  %-20s  %d\n",
			r.ID, r.Kind,
			r.TakenAt.Format("2006-01-02 15:04:05Z"),
			r.AppCount,
		)
	}
	return 0
}

// runSnapshotShow prints details for one snapshot by ID.
func runSnapshotShow(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra snapshot show <id>")
		return 2
	}
	id := fs.Arg(0)

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
	row, err := db.GetSnapshot(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(os.Stderr, "snapshot %q not found\n", id)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	apps, err := db.GetSnapshotApps(ctx, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"snapshot": row, "apps": apps})
		return 0
	}

	fmt.Printf("id:         %s\n", row.ID)
	fmt.Printf("kind:       %s\n", row.Kind)
	fmt.Printf("taken-at:   %s\n", row.TakenAt.Format("2006-01-02T15:04:05Z"))
	fmt.Printf("spectra:    %s\n", row.SpectraVer)
	fmt.Printf("apps:       %d\n\n", row.AppCount)

	if len(apps) > 0 {
		fmt.Printf("%-30s  %-16s  %-14s  %-10s  %s\n",
			"APP", "UI", "RUNTIME", "PACKAGING", "CONFIDENCE")
		fmt.Println(strings.Repeat("-", 88))
		for _, a := range apps {
			fmt.Printf("%-30s  %-16s  %-14s  %-10s  %s\n",
				truncate(a.AppName, 30), truncate(a.UI, 16),
				truncate(a.Runtime, 14), truncate(a.Packaging, 10),
				a.Confidence,
			)
		}
	}
	return 0
}

func printSnapshot(s snapshot.Snapshot) {
	fmt.Println("=== Spectra snapshot ===")
	fmt.Printf("id:             %s\n", s.ID)
	fmt.Printf("kind:           %s\n", s.Kind)
	fmt.Printf("taken-at:       %s\n", s.TakenAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Println()
	fmt.Print(s.Host.String())

	if len(s.Apps) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("apps:           %d inspected\n", len(s.Apps))

	byUI := map[string]int{}
	for _, a := range s.Apps {
		byUI[a.UI]++
	}
	keys := make([]string, 0, len(byUI))
	for k := range byUI {
		keys = append(keys, k)
	}
	sortStrings(keys)
	fmt.Println("by-ui:")
	for _, k := range keys {
		fmt.Printf("  %-26s %d\n", k, byUI[k])
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
