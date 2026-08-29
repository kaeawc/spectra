package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

type crashStateDeps struct {
	readFile      func(string) ([]byte, error)
	loadSnapshots func(host string) (hostname string, rows []store.SnapshotRow, err error)
	loadSnapshot  func(id string) (*snapshot.Snapshot, error)
}

// nearestSnapshot returns the snapshot closest in time to crashAt within
// window, and the signed delta (snapshot time minus crash time). ok is false
// when no snapshot falls inside the window.
func nearestSnapshot(crashAt time.Time, rows []store.SnapshotRow, window time.Duration) (store.SnapshotRow, time.Duration, bool) {
	var best store.SnapshotRow
	var bestAbs time.Duration
	found := false
	for _, r := range rows {
		delta := r.TakenAt.Sub(crashAt)
		abs := delta
		if abs < 0 {
			abs = -abs
		}
		if abs > window {
			continue
		}
		if !found || abs < bestAbs {
			best, bestAbs, found = r, abs, true
		}
	}
	if !found {
		return store.SnapshotRow{}, 0, false
	}
	return best, best.TakenAt.Sub(crashAt), true
}

func runCrashState(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash state", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	host := fs.String("host", "", "host whose snapshots to search (defaults to the only stored host)")
	window := fs.Duration("window", time.Hour, "max time between the crash and a usable snapshot")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash state [--json] [--host <h>] [--window 1h] <report.ips>")
		fmt.Fprintln(stderr, "Show what the machine was doing around a crash, from the nearest stored snapshot.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	return runCrashStateWithDeps(fs.Arg(0), *host, *window, *asJSON, stdout, stderr, defaultCrashStateDeps())
}

type crashStateOutput struct {
	Process   string   `json:"process"`
	Exception string   `json:"exception,omitempty"`
	CrashTime string   `json:"crash_time"`
	Snapshot  string   `json:"snapshot_id"`
	DeltaSec  float64  `json:"delta_seconds"`
	Thermal   string   `json:"thermal_pressure,omitempty"`
	Throttled bool     `json:"thermal_throttled"`
	TopByRSS  []string `json:"top_by_rss,omitempty"`
}

func runCrashStateWithDeps(path, host string, window time.Duration, asJSON bool, stdout, stderr io.Writer, deps crashStateDeps) int {
	data, err := deps.readFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "read %s: %v\n", path, err)
		return 1
	}
	report, err := crashreport.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "parse %s: %v\n", path, err)
		return 1
	}
	crashAt, err := time.Parse(ipsTimeLayout, report.Time)
	if err != nil {
		fmt.Fprintf(stderr, "%s has no parseable timestamp (%q)\n", path, report.Time)
		return 1
	}
	hostname, rows, err := deps.loadSnapshots(host)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	row, delta, ok := nearestSnapshot(crashAt, rows, window)
	if !ok {
		fmt.Fprintf(stderr, "no snapshot within %s of the crash on %s (%d snapshot(s) stored)\n", window, hostname, len(rows))
		return 1
	}
	snap, err := deps.loadSnapshot(row.ID)
	if err != nil {
		fmt.Fprintf(stderr, "load snapshot %s: %v\n", row.ID, err)
		return 1
	}
	if asJSON {
		return encodeJSON(stdout, stderr, buildCrashStateOutput(report, row, delta, snap))
	}
	renderCrashState(stdout, report, crashAt, hostname, row, delta, snap)
	return 0
}

func buildCrashStateOutput(r *crashreport.Report, row store.SnapshotRow, delta time.Duration, snap *snapshot.Snapshot) crashStateOutput {
	out := crashStateOutput{
		Process: r.Process, Exception: r.Exception, CrashTime: r.Time,
		Snapshot: row.ID, DeltaSec: delta.Seconds(),
		Thermal: snap.Power.ThermalPressure, Throttled: snap.Power.ThermalThrottled,
	}
	for _, p := range topNByRSS(snap.Processes, 5) {
		out.TopByRSS = append(out.TopByRSS, fmt.Sprintf("%s %dMB", p.Command, p.RSSKiB/1024))
	}
	return out
}

func topNByRSS(procs []process.Info, n int) []process.Info {
	sorted := append([]process.Info(nil), procs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RSSKiB > sorted[j].RSSKiB })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

func renderCrashState(w io.Writer, r *crashreport.Report, crashAt time.Time, hostname string, row store.SnapshotRow, delta time.Duration, snap *snapshot.Snapshot) {
	fmt.Fprintf(w, "%s crashed at %s", r.Process, crashAt.Format("2006-01-02 15:04:05"))
	if r.Exception != "" {
		fmt.Fprintf(w, " (%s)", r.Exception)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Nearest snapshot on %s: %s at %s (%s)\n",
		hostname, row.ID, row.TakenAt.Format("2006-01-02 15:04:05"), relativeDelta(delta))
	fmt.Fprintln(w, "Machine context at that snapshot (correlational, bounded by capture cadence):")
	if snap.Power.ThermalPressure != "" || snap.Power.ThermalThrottled {
		fmt.Fprintf(w, "  thermal: %s%s\n", naOr(snap.Power.ThermalPressure), throttleNote(snap.Power.ThermalThrottled))
	}
	top := topNByRSS(snap.Processes, 5)
	if len(top) > 0 {
		fmt.Fprintln(w, "  top processes by RSS:")
		for _, p := range top {
			fmt.Fprintf(w, "    %-28s %dMB\n", truncate(p.Command, 28), p.RSSKiB/1024)
		}
	}
}

func naOr(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

func throttleNote(throttled bool) string {
	if throttled {
		return " (throttled)"
	}
	return ""
}

func relativeDelta(delta time.Duration) string {
	if delta == 0 {
		return "at crash time"
	}
	mag := delta
	rel := "after the crash"
	if delta < 0 {
		mag = -delta
		rel = "before the crash"
	}
	return fmt.Sprintf("%s %s", mag.Round(time.Second), rel)
}

func defaultCrashStateDeps() crashStateDeps {
	return crashStateDeps{
		readFile:      os.ReadFile,
		loadSnapshots: storeSnapshotRows,
		loadSnapshot:  storeFullSnapshot,
	}
}

func storeSnapshotRows(host string) (string, []store.SnapshotRow, error) {
	db, cleanup, err := openStoreDB()
	if err != nil {
		return "", nil, err
	}
	defer cleanup()
	ctx := context.Background()
	hostRows, err := db.ListHosts(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("list hosts: %w", err)
	}
	hr, err := resolveBisectHost(hostRows, host)
	if err != nil {
		return "", nil, err
	}
	rows, err := db.ListSnapshots(ctx, hr.MachineUUID)
	if err != nil {
		return hr.Hostname, nil, fmt.Errorf("list snapshots: %w", err)
	}
	return hr.Hostname, rows, nil
}

func storeFullSnapshot(id string) (*snapshot.Snapshot, error) {
	db, cleanup, err := openStoreDB()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	data, err := db.GetSnapshotJSON(context.Background(), id)
	if err != nil {
		return nil, err
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func openStoreDB() (*store.DB, func(), error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve store path: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	return db, func() { _ = db.Close() }, nil
}
