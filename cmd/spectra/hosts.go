package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/store"
)

type hostLister func(context.Context) ([]store.HostRow, error)

func runHosts(args []string) int {
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
	return runHostsWith(args, os.Stdout, os.Stderr, db.ListHosts)
}

func runHostsWith(args []string, stdout io.Writer, stderr io.Writer, list hostLister) int {
	fs := flag.NewFlagSet("spectra hosts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: spectra hosts [--json]")
		return 2
	}

	rows, err := list(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintf(stderr, "encode hosts: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no hosts stored - run `spectra snapshot` first")
		return 0
	}
	printHostsTable(stdout, rows)
	return 0
}

func printHostsTable(w io.Writer, rows []store.HostRow) {
	fmt.Fprintf(w, "%-28s  %-20s  %-18s  %-7s  %s\n", "MACHINE UUID", "HOSTNAME", "OS", "SNAPS", "LAST SEEN")
	fmt.Fprintln(w, strings.Repeat("-", 92))
	for _, row := range rows {
		osLabel := strings.TrimSpace(row.OSName + " " + row.OSVersion)
		fmt.Fprintf(w, "%-28s  %-20s  %-18s  %-7d  %s\n",
			truncate(row.MachineUUID, 28),
			truncate(row.Hostname, 20),
			truncate(osLabel, 18),
			row.SnapshotCount,
			row.LastSeen.Format("2006-01-02 15:04:05Z"),
		)
	}
}
