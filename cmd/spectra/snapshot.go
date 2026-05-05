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
	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// snapshotSubcommands returns snapshot sub-subcommands. Separate function
// to mirror the top-level pattern and avoid init cycles.
func snapshotSubcommands() []subcommand {
	return []subcommand{
		{"list", "List stored snapshots", runSnapshotList},
		{"show", "Show details of one snapshot by ID", runSnapshotShow},
		{"diff", "Diff two stored snapshots", runSnapshotDiff},
		{"prune", "Delete live snapshots beyond the retention limit (default: keep 100)", runSnapshotPrune},
		{"baseline", "Manage baseline snapshots (list, drop)", runSnapshotBaseline},
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
	baseline := fs.Bool("baseline", false, "Save as a baseline (immutable; never auto-pruned)")
	name := fs.String("name", "", "Human label for the snapshot (most useful with --baseline)")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := snapshot.Options{
		SpectraVersion: version,
		DetectOpts:     detect.Options{ScanNetwork: *withNetwork},
		SkipApps:       *skipApps,
	}
	snap := snapshot.Build(context.Background(), opts)
	if *baseline {
		snap.Kind = snapshot.KindBaseline
	}

	if !*noStore {
		snapName := *name
		if err := saveSnapshotNamed(snap, snapName); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist snapshot: %v\n", err)
		}
		if *baseline && snapName != "" {
			fmt.Fprintf(os.Stderr, "baseline %q saved as %s\n", snapName, snap.ID)
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

// saveSnapshot opens the default DB, persists snap, and prunes old live
// snapshots beyond the default retention limit (100 per machine).
func saveSnapshot(snap snapshot.Snapshot) error {
	return saveSnapshotNamed(snap, "")
}

// saveSnapshotNamed persists snap with an optional human name label and prunes
// old live snapshots. Baselines are never pruned.
func saveSnapshotNamed(snap snapshot.Snapshot, name string) error {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	input := store.FromSnapshot(snap)
	input.Name = name
	if err := db.SaveSnapshot(ctx, input); err != nil {
		return err
	}
	if err := db.SaveSnapshotProcesses(ctx, snap.ID, store.ProcessesFromSnapshot(snap)); err != nil {
		return err
	}
	if err := db.SaveLoginItems(ctx, snap.ID, store.LoginItemsFromSnapshot(snap)); err != nil {
		return err
	}
	if err := db.SaveGrantedPerms(ctx, snap.ID, store.GrantedPermsFromSnapshot(snap)); err != nil {
		return err
	}
	if snap.Kind == snapshot.KindLive {
		_, _ = db.PruneSnapshots(ctx, 100) // best-effort; baselines skipped by PruneSnapshots
	}
	return nil
}

// runSnapshotList prints stored snapshots in a summary table.
func runSnapshotList(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	kindFilter := fs.String("kind", "", "Filter by kind: live or baseline")
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

	// Apply kind filter client-side (avoids schema change to ListSnapshots sig).
	if *kindFilter != "" {
		filtered := rows[:0]
		for _, r := range rows {
			if r.Kind == *kindFilter {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
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

	fmt.Printf("%-32s  %-8s  %-20s  %-20s  %s\n", "ID", "KIND", "TAKEN AT", "NAME", "APPS")
	fmt.Println(strings.Repeat("-", 90))
	for _, r := range rows {
		fmt.Printf("%-32s  %-8s  %-20s  %-20s  %d\n",
			r.ID, r.Kind,
			r.TakenAt.Format("2006-01-02 15:04:05Z"),
			truncate(r.Name, 20),
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

	// Try to use the full snapshot JSON for a rich display.
	if snapJSON, err := db.GetSnapshotJSON(ctx, id); err == nil && len(snapJSON) > 0 {
		var snap snapshot.Snapshot
		if err := json.Unmarshal(snapJSON, &snap); err == nil {
			if *asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(snap)
				return 0
			}
			printSnapshot(snap)
			return 0
		}
	}

	return runSnapshotShowFallback(ctx, db, row, id, *asJSON)
}

// runSnapshotShowFallback renders a snapshot using the lightweight row + apps
// when the full snapshot JSON blob is not available.
func runSnapshotShowFallback(ctx context.Context, db *store.DB, row store.SnapshotRow, id string, asJSON bool) int {
	apps, err := db.GetSnapshotApps(ctx, id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"snapshot": row, "apps": apps})
		return 0
	}

	fmt.Printf("id:         %s\n", row.ID)
	fmt.Printf("kind:       %s\n", row.Kind)
	if row.Name != "" {
		fmt.Printf("name:       %s\n", row.Name)
	}
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

	if len(s.Apps) > 0 {
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

	printSnapshotToolchains(s)
	printSnapshotNetwork(s)
	printSnapshotPower(s)
}

func printSnapshotToolchains(s snapshot.Snapshot) {
	tc := s.Toolchains
	if len(tc.JDKs) == 0 && len(tc.Node) == 0 && len(tc.Python) == 0 &&
		len(tc.Go) == 0 && len(tc.Ruby) == 0 && len(tc.Brew.Formulae) == 0 &&
		len(tc.BuildTools) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("toolchains:")
	if len(tc.JDKs) > 0 {
		fmt.Printf("  jdks:          %d installed\n", len(tc.JDKs))
	}
	if len(tc.Brew.Formulae) > 0 {
		fmt.Printf("  brew formulae: %d installed\n", len(tc.Brew.Formulae))
	}
	for _, rt := range []struct {
		name string
		list []toolchain.RuntimeInstall
	}{
		{"node", tc.Node}, {"python", tc.Python},
		{"go", tc.Go}, {"ruby", tc.Ruby},
	} {
		if len(rt.list) == 0 {
			continue
		}
		active := ""
		for _, r := range rt.list {
			if r.Active {
				active = " (active: " + r.Version + ")"
				break
			}
		}
		fmt.Printf("  %-14s %d version(s)%s\n", rt.name+":", len(rt.list), active)
	}
	if len(tc.BuildTools) > 0 {
		fmt.Printf("  build tools:   %d installed\n", len(tc.BuildTools))
	}
}

func printSnapshotNetwork(s snapshot.Snapshot) {
	n := s.Network
	if !n.VPNActive && len(n.ListeningPorts) == 0 && n.EstablishedConnectionsCount == 0 {
		return
	}
	fmt.Println()
	fmt.Println("network:")
	if n.VPNActive {
		fmt.Printf("  vpn:           active\n")
	}
	if n.EstablishedConnectionsCount > 0 {
		fmt.Printf("  connections:   %d established\n", n.EstablishedConnectionsCount)
	}
	if len(n.ListeningPorts) > 0 {
		fmt.Printf("  listening:     %d ports\n", len(n.ListeningPorts))
	}
}

func printSnapshotPower(s snapshot.Snapshot) {
	p := s.Power
	if p.ThermalPressure == "" && !p.OnBattery && p.BatteryPct == 0 {
		return
	}
	fmt.Println()
	fmt.Println("power:")
	if p.OnBattery {
		fmt.Printf("  source:        battery (%d%%)\n", p.BatteryPct)
	} else if p.BatteryPct > 0 {
		fmt.Printf("  battery:       %d%% (AC)\n", p.BatteryPct)
	}
	if p.ThermalPressure != "" && p.ThermalPressure != "nominal" {
		fmt.Printf("  thermal:       %s\n", p.ThermalPressure)
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// runSnapshotDiff loads two snapshots by ID and prints a structured diff.
// Either ID may be the sentinel "live" to capture a fresh snapshot on the fly.
func runSnapshotDiff(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot diff", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: spectra snapshot diff <id-a> <id-b|live>")
		return 2
	}
	idA, idB := fs.Arg(0), fs.Arg(1)

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
	snapA, err := resolveSnapshot(ctx, db, idA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot %q: %v\n", idA, err)
		return 1
	}
	snapB, err := resolveSnapshot(ctx, db, idB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot %q: %v\n", idB, err)
		return 1
	}

	result := diff.Compare(*snapA, *snapB)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}

	printDiff(result)
	return 0
}

// resolveSnapshot loads a snapshot from the DB by ID, or captures a fresh
// live snapshot when id is the sentinel "live".
func resolveSnapshot(ctx context.Context, db *store.DB, id string) (*snapshot.Snapshot, error) {
	if id == "live" {
		snap := snapshot.Build(ctx, snapshot.Options{SpectraVersion: version})
		return &snap, nil
	}
	return loadSnapshotFromDB(ctx, db, id)
}

// loadSnapshotFromDB retrieves the full snapshot JSON blob for id and unmarshals
// it into a snapshot.Snapshot. Returns an error if the snapshot is not found or
// was saved without a JSON blob (pre-v0.8 rows).
func loadSnapshotFromDB(ctx context.Context, db *store.DB, id string) (*snapshot.Snapshot, error) {
	raw, err := db.GetSnapshotJSON(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("snapshot exists but has no JSON blob (was it taken before this version?)")
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &s, nil
}

// runSnapshotPrune deletes live snapshots beyond the retention limit.
func runSnapshotPrune(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	keepN := fs.Int("keep", 100, "Number of live snapshots to retain per machine")
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

	deleted, err := db.PruneSnapshots(context.Background(), *keepN)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if deleted == 0 {
		fmt.Printf("nothing to prune (≤%d live snapshots)\n", *keepN)
	} else {
		fmt.Printf("pruned %d live snapshot(s) (keeping last %d per machine)\n", deleted, *keepN)
	}
	return 0
}

// runSnapshotBaseline dispatches to baseline sub-subcommands (list, drop).
func runSnapshotBaseline(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runBaselineList(args[1:])
		case "drop":
			return runBaselineDrop(args[1:])
		}
	}
	fmt.Fprintln(os.Stderr, "usage: spectra snapshot baseline <list|drop>")
	return 2
}

// runBaselineList lists baseline snapshots stored in the DB.
func runBaselineList(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot baseline list", flag.ContinueOnError)
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

	ctx := context.Background()
	rows, err := db.ListSnapshots(ctx, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var baselines []store.SnapshotRow
	for _, r := range rows {
		if r.Kind == "baseline" {
			baselines = append(baselines, r)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(baselines)
		return 0
	}

	if len(baselines) == 0 {
		fmt.Println("no baselines stored")
		return 0
	}
	fmt.Printf("%-40s  %-20s  %-4s  %s\n", "ID", "TAKEN AT", "APPS", "NAME")
	fmt.Println(strings.Repeat("-", 90))
	for _, r := range baselines {
		name := r.Name
		if name == "" {
			name = "(unnamed)"
		}
		fmt.Printf("%-40s  %-20s  %-4d  %s\n",
			r.ID, r.TakenAt.Format("2006-01-02T15:04:05Z"), r.AppCount, name)
	}
	return 0
}

// runBaselineDrop deletes a baseline snapshot by ID.
func runBaselineDrop(args []string) int {
	fs := flag.NewFlagSet("spectra snapshot baseline drop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra snapshot baseline drop <id>")
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
		fmt.Fprintf(os.Stderr, "baseline %q not found\n", id)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if row.Kind != "baseline" {
		fmt.Fprintf(os.Stderr, "%q is a %q snapshot, not a baseline — use 'snapshot prune' for live snapshots\n", id, row.Kind)
		return 1
	}

	if err := db.DeleteSnapshot(ctx, id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("dropped baseline %s\n", id)
	return 0
}

// printDiff renders a diff.Result as a human-readable table.
func printDiff(r diff.Result) {
	if !r.HasChanges() {
		fmt.Println("no differences")
		return
	}
	fmt.Printf("diff  %s  →  %s\n\n", r.AID, r.BID)
	for _, sec := range r.Sections {
		if len(sec.Changes) == 0 {
			continue
		}
		fmt.Printf("=== %s ===\n", sec.Name)
		for _, c := range sec.Changes {
			switch c.Kind {
			case diff.Added:
				fmt.Printf("  + %-40s  %s\n", c.Key, c.After)
			case diff.Removed:
				fmt.Printf("  - %-40s  %s\n", c.Key, c.Before)
			case diff.Changed:
				fmt.Printf("  ~ %-40s  %s  →  %s\n", c.Key, c.Before, c.After)
			}
		}
		fmt.Println()
	}
}
