package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/snapshotquery"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// snapshotSubcommands returns snapshot sub-subcommands. Separate function
// to mirror the top-level pattern and avoid init cycles.
func snapshotSubcommands() []subcommand {
	return []subcommand{
		{"create", "Capture a structured snapshot", runSnapshotCreate},
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
	assessGatekeeper := fs.Bool("assess-gatekeeper", false, "Run the real spctl Gatekeeper assessment (slow; serialized, 15s timeout per app). Default infers it from the code signature")
	skipApps := fs.Bool("no-apps", false, "Skip the apps inventory; capture host info only")
	noStore := fs.Bool("no-store", false, "Do not persist the snapshot to the local database")
	baseline := fs.Bool("baseline", false, "Save as a baseline (immutable; never auto-pruned)")
	name := fs.String("name", "", "Human label for the snapshot (most useful with --baseline)")
	fresh := fs.Bool("fresh", false, "Re-walk ~/Library instead of using a footprint cached within the last 10 minutes")

	if err := fs.Parse(args); err != nil {
		return 2
	}
	snapName, ok := snapshotNameFromArgs(*baseline, *name, fs.Args())
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: spectra snapshot [--baseline [name]] [--name name]")
		return 2
	}

	opts := snapshot.Options{
		SpectraVersion: version,
		DetectOpts:     detect.Options{ScanNetwork: *withNetwork, AssessGatekeeper: *assessGatekeeper, SealCache: sealCache()},
		SkipApps:       *skipApps,
	}
	withCLIFootprintCache(&opts, *fresh)
	defer withCLIDetectCache(&opts, *withNetwork || *fresh)()
	snap := snapshot.Build(context.Background(), opts)
	if *baseline {
		snap.Kind = snapshot.KindBaseline
	}

	if !*noStore {
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

// withCLIFootprintCache serves the snapshot's ~/Library footprint from the
// cache `spectra storage` uses, with the same policy, so a snapshot taken
// soon after a storage run (or another snapshot) skips the full walk. The
// fresh-only default keeps persisted footprints within 10 minutes of capture.
func withCLIFootprintCache(opts *snapshot.Options, fresh bool) {
	opts.FootprintCache, opts.FootprintPolicy = cliFootprintCache(fresh)
}

// withCLIDetectCache serves per-app inspection from the persistent detect
// cache the rules path uses, so an unchanged bundle is not re-walked, re-hashed
// and re-classified on every snapshot. A hit always recollects processes,
// uptime, login items and TCC grants; the bundle size and ~/Library
// footprint are also measured live here because snapshots are persisted and
// diffed. bypass skips the cache: the key does not cover --network, and
// --fresh asks for a full re-inspection. The returned func flushes pending
// cache writes.
func withCLIDetectCache(opts *snapshot.Options, bypass bool) func() {
	if cacheStores == nil || cacheStores.Detect == nil || bypass {
		return func() {}
	}
	w := cache.NewAsyncWriter(cacheStores.Detect, 64, 2)
	opts.DetectStore, opts.DetectWriter, opts.DetectRefreshStorage = cacheStores.Detect, w, true
	return w.Close
}

func runSnapshotCreate(args []string) int {
	return runSnapshot(args)
}

func snapshotNameFromArgs(baseline bool, explicitName string, args []string) (string, bool) {
	if len(args) == 0 {
		return explicitName, true
	}
	if !baseline || explicitName != "" || len(args) > 1 {
		return "", false
	}
	return args[0], true
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
	return saveSnapshotToRegistry(context.Background(), db, snap, name)
}

func saveSnapshotToRegistry(ctx context.Context, db snapshotRegistry, snap snapshot.Snapshot, name string) error {
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

// snapshotReadDeps open the snapshot store and capture live snapshots for
// the read-only snapshot commands; tests substitute them.
type snapshotReadDeps struct {
	open func() (snapshotquery.Reader, func(), error)
	live func(context.Context) *snapshot.Snapshot
	now  func() time.Time
}

func defaultSnapshotReadDeps() snapshotReadDeps {
	return snapshotReadDeps{
		open: func() (snapshotquery.Reader, func(), error) {
			dbPath, err := store.DefaultPath()
			if err != nil {
				return nil, nil, err
			}
			db, err := store.Open(dbPath)
			if err != nil {
				return nil, nil, err
			}
			return db, func() { db.Close() }, nil
		},
		live: func(ctx context.Context) *snapshot.Snapshot { return collectLiveSnapshot(ctx) },
		now:  time.Now,
	}
}

// runSnapshotList prints stored snapshots in a summary table.
func runSnapshotList(args []string) int {
	return runSnapshotListWithIO(args, os.Stdout, os.Stderr, defaultSnapshotReadDeps())
}

func runSnapshotListWithIO(args []string, stdout, stderr io.Writer, deps snapshotReadDeps) int {
	fs := flag.NewFlagSet("spectra snapshot list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	kindFilter := fs.String("kind", "", "Filter by kind: live or baseline")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	db, closeDB, err := deps.open()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeDB()

	rows, err := snapshotquery.List(context.Background(), db, *kindFilter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no snapshots stored — run `spectra snapshot` first")
		if !*asJSON {
			return 0
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	fmt.Fprintf(stdout, "%-32s  %-8s  %-8s  %-20s  %-20s  %s\n", "ID", "KIND", "TAG", "TAKEN AT", "NAME", "APPS")
	fmt.Fprintln(stdout, strings.Repeat("-", 100))
	for _, r := range rows {
		tag := r.Tag
		if tag == "" {
			tag = "-"
		}
		fmt.Fprintf(stdout, "%-32s  %-8s  %-8s  %-20s  %-20s  %d\n",
			r.ID, r.Kind,
			tag,
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
	if row.Tag != "" {
		fmt.Printf("tag:        %s\n", row.Tag)
	}
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
	if s.Tag != "" {
		fmt.Printf("tag:            %s\n", s.Tag)
	}
	fmt.Printf("taken-at:       %s\n", s.TakenAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Println()
	fmt.Print(s.Host.String())
	printSnapshotHostFacts(s.HostFacts)
	if !s.Host.Memory.CollectedAt.IsZero() {
		fmt.Print("memory:         ")
		printMemoryState(s.Host.Memory, memstate.MemoryState{})
	}
	if !s.Host.TimeMachine.IsZero() {
		fmt.Printf("time-machine:   destinations=%d local-snapshots=%d scheduler=%s\n",
			len(s.Host.TimeMachine.Destinations),
			len(s.Host.TimeMachine.LocalSnapshots),
			loadedString(s.Host.TimeMachine.SchedulerLoaded),
		)
	}

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

func printSnapshotHostFacts(f snapshot.HostFacts) {
	if f.Hostname == "" && f.KernelVersion == "" && f.BootUUID == "" && f.LoadAverages.At.IsZero() {
		return
	}
	if f.KernelVersion != "" {
		fmt.Printf("kernel:         %s\n", f.KernelVersion)
	}
	if !f.BootTime.IsZero() {
		fmt.Printf("boot-time:      %s\n", f.BootTime.Format(time.RFC3339))
	}
	if f.BootUUID != "" {
		fmt.Printf("boot-uuid:      %s\n", f.BootUUID)
	}
	if !f.LoadAverages.At.IsZero() {
		fmt.Printf("load-average:   %.2f %.2f %.2f\n",
			f.LoadAverages.OneMinute,
			f.LoadAverages.FiveMinute,
			f.LoadAverages.FifteenMinute,
		)
	}
	if len(f.RecentReboots) > 0 {
		fmt.Printf("recent-reboots: %d\n", len(f.RecentReboots))
	}
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

// runSnapshotDiff loads two snapshots and prints a structured diff.
// Either ID may be the sentinel "live" to capture a fresh snapshot on the fly.
// It also supports `diff baseline [name|id] [live|id]`, where the baseline
// reference is optional and resolves to the newest baseline when omitted.
func runSnapshotDiff(args []string) int {
	return runSnapshotDiffWithIO(args, os.Stdout, os.Stderr, defaultSnapshotReadDeps())
}

func runSnapshotDiffWithIO(args []string, stdout, stderr io.Writer, deps snapshotReadDeps) int {
	fs := flag.NewFlagSet("spectra snapshot diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	since := fs.Duration("since", 0, "Diff the most recent stored snapshot older than this duration against live")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *since < 0 {
		fmt.Fprintln(stderr, "spectra snapshot diff: --since must be positive")
		return 2
	}
	opts, err := snapshotquery.DiffOptionsFromArgs(fs.Args(), *since)
	if err != nil {
		if *since > 0 {
			fmt.Fprintln(stderr, "spectra snapshot diff: --since cannot be combined with explicit snapshot IDs")
		} else {
			printDiffUsage(stderr)
		}
		return 2
	}

	db, closeDB, err := deps.open()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer closeDB()

	result, err := snapshotquery.Diff(context.Background(), db, opts, snapshotquery.DiffDeps{Live: deps.live, Now: deps.now})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}

	printDiff(result)
	return 0
}

func printDiffUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: spectra snapshot diff <id-a> <id-b|live>")
	fmt.Fprintln(w, "   or: spectra snapshot diff --since <duration>")
	fmt.Fprintln(w, "   or: spectra diff baseline [name|id] [live|id]")
}

var collectLiveSnapshot = func(ctx context.Context) *snapshot.Snapshot {
	snap := snapshot.Build(ctx, snapshot.Options{SpectraVersion: version})
	return &snap
}

type snapshotRegistry interface {
	SaveSnapshot(context.Context, store.SnapshotInput) error
	SaveSnapshotProcesses(context.Context, string, []store.ProcessSnapshotRow) error
	SaveLoginItems(context.Context, string, []store.LoginItemRow) error
	SaveGrantedPerms(context.Context, string, []store.GrantedPermRow) error
	PruneSnapshots(context.Context, int) (int64, error)
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
