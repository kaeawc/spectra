package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/fleet"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

// seriesLoader returns a host's snapshot series oldest→newest and the resolved
// hostname.
type seriesLoader func(hostname string) ([]snapshot.Snapshot, string, error)

func runBisect(args []string) int {
	return runBisectWithIO(args, os.Stdout, os.Stderr, defaultSeriesLoader)
}

func runBisectWithIO(args []string, stdout, stderr io.Writer, load seriesLoader) int {
	fs := flag.NewFlagSet("bisect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	host := fs.String("host", "", "host to bisect (defaults to the only stored host)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra bisect [--json] [--host <hostname>] <rule-id>")
		fmt.Fprintln(stderr, "Find the snapshot where a rule started firing, and what changed alongside it.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	ruleID := fs.Arg(0)
	if !ruleInCatalog(ruleID) {
		fmt.Fprintf(stderr, "unknown rule id %q — run `spectra rules` to see available rules\n", ruleID)
		return 2
	}
	series, hostname, err := load(*host)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if len(series) == 0 {
		fmt.Fprintf(stderr, "no snapshots stored for host %q\n", hostname)
		return 1
	}
	res := fleet.BisectSymptom(series, ruleID, rules.V1Catalog())
	if *asJSON {
		return encodeJSON(stdout, stderr, res)
	}
	renderBisect(stdout, res, hostname)
	return 0
}

func defaultSeriesLoader(hostname string) ([]snapshot.Snapshot, string, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, "", fmt.Errorf("resolve store path: %w", err)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, "", fmt.Errorf("open store %s: %w", dbPath, err)
	}
	defer db.Close()
	ctx := context.Background()
	hostRows, err := db.ListHosts(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("list hosts: %w", err)
	}
	hr, err := resolveBisectHost(hostRows, hostname)
	if err != nil {
		return nil, "", err
	}
	snaps, err := db.ListSnapshots(ctx, hr.MachineUUID)
	if err != nil {
		return nil, "", fmt.Errorf("list snapshots: %w", err)
	}
	// ListSnapshots is newest-first; reverse into an oldest-first series.
	series := make([]snapshot.Snapshot, 0, len(snaps))
	for i := len(snaps) - 1; i >= 0; i-- {
		data, gerr := db.GetSnapshotJSON(ctx, snaps[i].ID)
		if gerr != nil {
			continue
		}
		var s snapshot.Snapshot
		if json.Unmarshal(data, &s) == nil {
			series = append(series, s)
		}
	}
	return series, hr.Hostname, nil
}

func resolveBisectHost(rows []store.HostRow, hostname string) (store.HostRow, error) {
	if len(rows) == 0 {
		return store.HostRow{}, errors.New("no hosts in the store — run `spectra snapshot` first")
	}
	if hostname != "" {
		for _, r := range rows {
			if r.Hostname == hostname {
				return r, nil
			}
		}
		return store.HostRow{}, fmt.Errorf("no stored host named %q", hostname)
	}
	if len(rows) == 1 {
		return rows[0], nil
	}
	return store.HostRow{}, errors.New("multiple hosts stored — specify --host <hostname>")
}

func renderBisect(w io.Writer, r fleet.BisectResult, hostname string) {
	switch r.Status {
	case "clean":
		fmt.Fprintf(w, "%s never fires across the %d snapshot(s) stored for %s.\n", r.RuleID, r.Snapshots, hostname)
		return
	case "already-firing":
		fmt.Fprintf(w, "%s is already firing in the oldest of %d snapshot(s) for %s (%s, %s).\n",
			r.RuleID, r.Snapshots, hostname, r.FirstBadID, r.FirstBadTime.Format("2006-01-02 15:04"))
		fmt.Fprintln(w, "No earlier snapshot to bisect against — capture started after the symptom.")
		return
	}
	fmt.Fprintf(w, "%s first tripped in %s (%s) on %s.\n",
		r.RuleID, r.FirstBadID, r.FirstBadTime.Format("2006-01-02 15:04"), hostname)
	fmt.Fprintf(w, "Changes that co-occurred in that snapshot vs the prior one (%s) — correlated, not necessarily the cause:\n", r.PrevID)
	if len(r.Changes) == 0 {
		fmt.Fprintln(w, "  (no other tracked changes in that snapshot)")
	}
	for _, c := range r.Changes {
		fmt.Fprintf(w, "  [%s/%s] %s%s\n", c.Section, c.Kind, c.Key, beforeAfter(c))
	}
	fmt.Fprintln(w, "Note: snapshots are periodic, so timing is bounded by capture cadence.")
}

func beforeAfter(c fleet.CoChange) string {
	switch {
	case c.Before != "" && c.After != "":
		return fmt.Sprintf(" %s -> %s", c.Before, c.After)
	case c.After != "":
		return " +" + c.After
	case c.Before != "":
		return " -" + c.Before
	default:
		return ""
	}
}
