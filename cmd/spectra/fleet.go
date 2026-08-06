package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/fleet"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

type fleetLoader func() ([]fleet.HostSnapshot, error)

func runFleet(args []string) int {
	return runFleetWithIO(args, os.Stdout, os.Stderr, defaultFleetLoader)
}

func runFleetWithIO(args []string, stdout, stderr io.Writer, load fleetLoader) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stderr, "usage: spectra fleet <symptom <rule-id> | drift (--jdk | --app <bundleID>)> [--json]")
		return 2
	}
	switch args[0] {
	case "symptom":
		return runFleetSymptom(args[1:], stdout, stderr, load)
	case "drift":
		return runFleetDrift(args[1:], stdout, stderr, load)
	default:
		fmt.Fprintf(stderr, "unknown fleet subcommand %q (want: symptom, drift)\n", args[0])
		return 2
	}
}

// defaultFleetLoader loads each host's latest snapshot from the local store.
func defaultFleetLoader() ([]fleet.HostSnapshot, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx := context.Background()
	hostRows, err := db.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]fleet.HostSnapshot, 0, len(hostRows))
	for _, hr := range hostRows {
		out = append(out, loadHostSnapshot(ctx, db, hr))
	}
	return out, nil
}

func loadHostSnapshot(ctx context.Context, db *store.DB, hr store.HostRow) fleet.HostSnapshot {
	hs := fleet.HostSnapshot{Hostname: hr.Hostname, MachineUUID: hr.MachineUUID}
	snaps, err := db.ListSnapshots(ctx, hr.MachineUUID)
	if err != nil || len(snaps) == 0 {
		hs.Empty = true
		return hs
	}
	data, err := db.GetSnapshotJSON(ctx, snaps[0].ID)
	if err != nil {
		hs.Empty = true
		return hs
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		hs.Empty = true
		return hs
	}
	hs.Snap = snap
	return hs
}

func runFleetSymptom(args []string, stdout, stderr io.Writer, load fleetLoader) int {
	fs := flag.NewFlagSet("fleet symptom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra fleet symptom [--json] <rule-id>")
		return 2
	}
	hosts, code := loadFleet(load, stderr)
	if code != 0 {
		return code
	}
	rollup := fleet.RollupSymptom(hosts, fs.Arg(0), rules.V1Catalog())
	if *asJSON {
		return encodeJSON(stdout, stderr, rollup)
	}
	fmt.Fprintf(stdout, "symptom %s across %d host(s):\n", rollup.RuleID, len(hosts))
	fmt.Fprintf(stdout, "  firing (%d): %s\n", len(rollup.Firing), joinOrDash(rollup.Firing))
	fmt.Fprintf(stdout, "  clear  (%d): %s\n", len(rollup.Clear), joinOrDash(rollup.Clear))
	if len(rollup.Unknown) > 0 {
		fmt.Fprintf(stdout, "  unknown (%d): %s\n", len(rollup.Unknown), joinOrDash(rollup.Unknown))
	}
	return 0
}

func runFleetDrift(args []string, stdout, stderr io.Writer, load fleetLoader) int {
	fs := flag.NewFlagSet("fleet drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	jdk := fs.Bool("jdk", false, "JDK version drift")
	app := fs.String("app", "", "app bundle ID version drift")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*jdk && *app == "" {
		fmt.Fprintln(stderr, "usage: spectra fleet drift (--jdk | --app <bundleID>) [--json]")
		return 2
	}
	hosts, code := loadFleet(load, stderr)
	if code != 0 {
		return code
	}
	var cells []fleet.DriftCell
	dim := "jdk"
	if *jdk {
		cells = fleet.DriftJDK(hosts)
	} else {
		cells = fleet.DriftApp(hosts, *app)
		dim = *app
	}
	if *asJSON {
		return encodeJSON(stdout, stderr, cells)
	}
	fmt.Fprintf(stdout, "%s drift across %d host(s):\n", dim, len(cells))
	for _, c := range cells {
		fmt.Fprintf(stdout, "  %-24s %s\n", c.Host, c.Value)
	}
	return 0
}

func loadFleet(load fleetLoader, stderr io.Writer) ([]fleet.HostSnapshot, int) {
	hosts, err := load()
	if err != nil {
		fmt.Fprintf(stderr, "load fleet: %v\n", err)
		return nil, 1
	}
	if len(hosts) == 0 {
		fmt.Fprintln(stderr, "no hosts in the store — run `spectra snapshot` (and fan-out to peers) first")
		return nil, 1
	}
	return hosts, 0
}

func encodeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func joinOrDash(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	out := xs[0]
	for _, x := range xs[1:] {
		out += ", " + x
	}
	return out
}
