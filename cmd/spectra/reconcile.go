package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/fleet"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/toolchain"
)

type toolchainPair struct {
	FromTC   toolchain.Toolchains
	ToTC     toolchain.Toolchains
	FromName string
	ToName   string
}

type reconcileLoader func(from, target string) (toolchainPair, error)

func runReconcile(args []string) int {
	return runReconcileWithIO(args, os.Stdout, os.Stderr, defaultReconcileLoader)
}

func runReconcileWithIO(args []string, stdout, stderr io.Writer, load reconcileLoader) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	from := fs.String("from", "", "source host to change (defaults to this machine)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra reconcile [--json] [--from <host>] <target-host>")
		fmt.Fprintln(stderr, "Print an advisory plan to make one host's toolchain match another's. Nothing is executed.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	pair, err := load(*from, fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	steps := fleet.Reconcile(pair.FromTC, pair.ToTC)
	if *asJSON {
		return encodeJSON(stdout, stderr, struct {
			From  string                `json:"from"`
			To    string                `json:"to"`
			Steps []fleet.ReconcileStep `json:"steps"`
		}{pair.FromName, pair.ToName, steps})
	}
	renderReconcile(stdout, pair.FromName, pair.ToName, steps)
	return 0
}

func defaultReconcileLoader(from, target string) (toolchainPair, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return toolchainPair{}, fmt.Errorf("resolve store path: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return toolchainPair{}, fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer db.Close()
	ctx := context.Background()
	rows, err := db.ListHosts(ctx)
	if err != nil {
		return toolchainPair{}, fmt.Errorf("list hosts: %w", err)
	}
	if from == "" {
		if h, herr := os.Hostname(); herr == nil {
			from = h
		}
	}
	fromHR, err := resolveBisectHost(rows, from)
	if err != nil {
		return toolchainPair{}, fmt.Errorf("source host: %w", err)
	}
	toHR, err := resolveBisectHost(rows, target)
	if err != nil {
		return toolchainPair{}, fmt.Errorf("target host: %w", err)
	}
	fromTC, err := loadHostToolchains(ctx, db, fromHR)
	if err != nil {
		return toolchainPair{}, err
	}
	toTC, err := loadHostToolchains(ctx, db, toHR)
	if err != nil {
		return toolchainPair{}, err
	}
	return toolchainPair{FromTC: fromTC, ToTC: toTC, FromName: fromHR.Hostname, ToName: toHR.Hostname}, nil
}

func loadHostToolchains(ctx context.Context, db *store.DB, hr store.HostRow) (toolchain.Toolchains, error) {
	snaps, err := db.ListSnapshots(ctx, hr.MachineUUID)
	if err != nil {
		return toolchain.Toolchains{}, fmt.Errorf("list snapshots for %s: %w", hr.Hostname, err)
	}
	if len(snaps) == 0 {
		return toolchain.Toolchains{}, fmt.Errorf("no snapshots stored for %s", hr.Hostname)
	}
	data, err := db.GetSnapshotJSON(ctx, snaps[0].ID)
	if err != nil {
		return toolchain.Toolchains{}, fmt.Errorf("load snapshot for %s: %w", hr.Hostname, err)
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return toolchain.Toolchains{}, fmt.Errorf("parse snapshot for %s: %w", hr.Hostname, err)
	}
	return s.Toolchains, nil
}

func renderReconcile(w io.Writer, fromName, toName string, steps []fleet.ReconcileStep) {
	if len(steps) == 0 {
		fmt.Fprintf(w, "%s's toolchain already matches %s.\n", fromName, toName)
		return
	}
	fmt.Fprintf(w, "DRY RUN — advisory plan to make %s match %s.\n", fromName, toName)
	fmt.Fprintln(w, "Nothing here runs automatically; these are descriptions, not commands — verify each before acting.")
	for i, s := range steps {
		fmt.Fprintf(w, "%d) [%s/%s] %s\n", i+1, s.Category, s.Action, s.Detail)
	}
}
