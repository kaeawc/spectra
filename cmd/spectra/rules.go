package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

func runRules(args []string) int {
	fs := flag.NewFlagSet("spectra rules", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	snapID := fs.String("snapshot", "", "Evaluate against a stored snapshot by ID (default: take a live snapshot)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var snap snapshot.Snapshot
	if *snapID != "" {
		s, err := loadStoredSnapshot(*snapID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rules: %v\n", err)
			return 1
		}
		snap = *s
	} else {
		snap = snapshot.Build(context.Background(), snapshot.Options{
			SpectraVersion: version,
			DetectOpts:     detect.Options{},
		})
	}

	findings := rules.Evaluate(snap, rules.V1Catalog())

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(findings)
		return 0
	}

	if len(findings) == 0 {
		fmt.Println("no findings — all rules passed")
		return 0
	}

	printFindings(findings)
	return 0
}

func loadStoredSnapshot(id string) (*snapshot.Snapshot, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	raw, err := db.GetSnapshotJSON(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("snapshot %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("snapshot %q has no JSON blob (taken before this version)", id)
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot %q: %w", id, err)
	}
	return &s, nil
}

func printFindings(findings []rules.Finding) {
	fmt.Printf("%-8s  %-30s  %s\n", "SEVERITY", "RULE", "MESSAGE")
	fmt.Println(strings.Repeat("-", 90))
	for _, f := range findings {
		msg := f.Message
		if f.Subject != "" {
			msg = fmt.Sprintf("[%s] %s", f.Subject, msg)
		}
		fmt.Printf("%-8s  %-30s  %s\n", f.Severity, truncate(f.RuleID, 30), msg)
	}
	fmt.Printf("\n%d finding(s)\n", len(findings))
}
