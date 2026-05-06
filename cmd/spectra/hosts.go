package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

type hostLister func(context.Context, bool, bool) ([]store.HostRow, error)
type hostProber func(ctx context.Context, host string) error

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
	return runHostsWith(args, os.Stdout, os.Stderr, func(ctx context.Context, discover bool, discoverDaemons bool) ([]store.HostRow, error) {
		return listHostRows(ctx, db, discover, discoverDaemons)
	}, probeHost)
}

func runHostsWith(args []string, stdout io.Writer, stderr io.Writer, list hostLister, probe hostProber) int {
	fs := flag.NewFlagSet("spectra hosts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	probeFlag := fs.Bool("probe", false, "Probe each known host and report reachability")
	discoverFlag := fs.Bool("discover", false, "Merge tailscale peer discovery from `tailscale status --json`")
	discoverDaemonsFlag := fs.Bool("discover-daemons", false, "Discover reachable Spectra daemons from Tailscale peers")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: spectra hosts [--json] [--probe] [--discover|--discover-daemons]")
		return 2
	}

	rows, err := list(context.Background(), *discoverFlag, *discoverDaemonsFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		if *probeFlag && probe == nil {
			return runHostsJSON(stdout, stderr, rows)
		}
		if !*probeFlag {
			return runHostsJSON(stdout, stderr, rows)
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		results := probeHostRows(context.Background(), rows, probe)
		if err := enc.Encode(results); err != nil {
			fmt.Fprintf(stderr, "encode hosts: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no hosts stored - run `spectra snapshot` first")
		return 0
	}
	if *probeFlag {
		if probe == nil {
			printHostsTable(stdout, rows)
			return 0
		}
		printHostsProbeTable(stdout, probeHostRows(context.Background(), rows, probe))
		return 0
	}
	printHostsTable(stdout, rows)
	return 0
}

func runHostsJSON(stdout io.Writer, stderr io.Writer, rows []store.HostRow) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rows); err != nil {
		fmt.Fprintf(stderr, "encode hosts: %v\n", err)
		return 1
	}
	return 0
}

func probeHostRows(ctx context.Context, rows []store.HostRow, probe hostProber) []hostProbeRow {
	out := make([]hostProbeRow, 0, len(rows))
	for _, row := range rows {
		pr := hostProbeRow{HostRow: row}
		if row.Hostname == "" {
			pr.Reachable = false
			pr.Error = "empty hostname"
			out = append(out, pr)
			continue
		}
		if err := probe(ctx, row.Hostname); err != nil {
			pr.Reachable = false
			pr.Error = err.Error()
		} else {
			pr.Reachable = true
		}
		out = append(out, pr)
	}
	return out
}

func probeHost(ctx context.Context, rawHost string) error {
	target, err := parseConnectTarget(rawHost)
	if err != nil {
		return err
	}
	conn, err := dialConnectTarget(target, 3*time.Second)
	if err != nil {
		return fmt.Errorf("connect %q: %w", rawHost, err)
	}
	defer conn.Close()

	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(3 * time.Second))
	}
	if _, err := callRPC(conn, "health", nil); err != nil {
		return fmt.Errorf("health: %w", err)
	}
	return nil
}

type hostProbeRow struct {
	store.HostRow
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

var discoverHostRowsNow = time.Now
var discoverHostProbe hostProber = probeHost

func listHostRows(ctx context.Context, db *store.DB, discover bool, discoverDaemons bool) ([]store.HostRow, error) {
	rows, err := db.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	out := make([]store.HostRow, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Hostname)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, row)
	}
	if discover || discoverDaemons {
		discovered, err := fanDiscoverPeers()
		if err != nil && len(rows) == 0 {
			return nil, fmt.Errorf("discover remote hosts: %w", err)
		}
		for _, host := range discovered {
			name := strings.TrimSpace(host)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			if discoverDaemons {
				if err := discoverHostProbe(ctx, name); err != nil {
					continue
				}
			}
			seen[name] = struct{}{}
			out = append(out, store.HostRow{
				MachineUUID: name,
				Hostname:    name,
				LastSeen:    discoverHostRowsNow().UTC(),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Hostname < out[j].Hostname
	})
	return out, nil
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

func printHostsProbeTable(w io.Writer, rows []hostProbeRow) {
	fmt.Fprintf(w, "%-28s  %-20s  %-18s  %-7s  %-9s  %s\n", "MACHINE UUID", "HOSTNAME", "OS", "SNAPS", "REACHABLE", "ERROR")
	fmt.Fprintln(w, strings.Repeat("-", 110))
	for _, r := range rows {
		osLabel := strings.TrimSpace(r.OSName + " " + r.OSVersion)
		reachable := "no"
		if r.Reachable {
			reachable = "yes"
		}
		fmt.Fprintf(w, "%-28s  %-20s  %-18s  %-7d  %-9s  %s\n",
			truncate(r.MachineUUID, 28),
			truncate(r.Hostname, 20),
			truncate(osLabel, 18),
			r.SnapshotCount,
			reachable,
			r.Error,
		)
	}
}
