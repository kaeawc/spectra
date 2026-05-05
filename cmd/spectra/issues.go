package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

func issuesSubcommands() []subcommand {
	return []subcommand{
		{"list", "List stored issues", runIssuesList},
		{"update", "Update an issue status", runIssuesUpdate},
		{"acknowledge", "Mark an issue acknowledged", runIssuesAcknowledge},
		{"dismiss", "Mark an issue dismissed", runIssuesDismiss},
		{"check", "Run rules, record findings as issues, and print a summary", runIssuesCheck},
	}
}

func runIssues(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		for _, sc := range issuesSubcommands() {
			if args[0] == sc.name {
				return sc.run(args[1:])
			}
		}
	}
	// Default: list open issues.
	return runIssuesList(args)
}

// runIssuesList prints stored issues, optionally filtered by status.
func runIssuesList(args []string) int {
	fs := flag.NewFlagSet("spectra issues list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	status := fs.String("status", "", "Filter by status: open, acknowledged, dismissed, fixed, closed")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	db, machineUUID, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	rows, err := db.ListIssues(context.Background(), machineUUID, store.IssueStatus(*status))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	if len(rows) == 0 {
		if *status != "" {
			fmt.Printf("no %s issues\n", *status)
		} else {
			fmt.Println("no issues — run `spectra issues check` to evaluate rules")
		}
		return 0
	}

	fmt.Printf("%-20s  %-8s  %-10s  %-30s  %s\n", "ID", "SEVERITY", "STATUS", "RULE", "MESSAGE")
	fmt.Println(strings.Repeat("-", 100))
	for _, r := range rows {
		msg := r.Message
		if r.Subject != "" {
			msg = fmt.Sprintf("[%s] %s", r.Subject, msg)
		}
		fmt.Printf("%-20s  %-8s  %-10s  %-30s  %s\n",
			truncate(r.ID, 20), r.Severity, string(r.Status),
			truncate(r.RuleID, 30), truncate(msg, 60),
		)
	}
	fmt.Printf("\n%d issue(s)\n", len(rows))
	return 0
}

// runIssuesUpdate changes an issue's status.
func runIssuesUpdate(args []string) int {
	fs := flag.NewFlagSet("spectra issues update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	status := fs.String("status", "", "New status: open, acknowledged, dismissed, fixed, closed")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 || *status == "" {
		fmt.Fprintln(os.Stderr, "usage: spectra issues update --status <status> <issue-id>")
		return 2
	}

	db, _, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	id := fs.Arg(0)
	if err := db.UpdateIssueStatus(context.Background(), id, store.IssueStatus(*status)); err != nil {
		fmt.Fprintf(os.Stderr, "update %q: %v\n", id, err)
		return 1
	}
	fmt.Printf("issue %s → %s\n", id, *status)
	return 0
}

func runIssuesAcknowledge(args []string) int {
	return runIssuesSetStatus(args, "acknowledge", store.IssueAcknowledged)
}

func runIssuesDismiss(args []string) int {
	return runIssuesSetStatus(args, "dismiss", store.IssueDismissed)
}

func runIssuesSetStatus(args []string, verb string, status store.IssueStatus) int {
	fs := flag.NewFlagSet("spectra issues "+verb, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: spectra issues %s <issue-id>\n", verb)
		return 2
	}

	db, _, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	id := fs.Arg(0)
	if err := db.UpdateIssueStatus(context.Background(), id, status); err != nil {
		fmt.Fprintf(os.Stderr, "%s %q: %v\n", verb, id, err)
		return 1
	}
	fmt.Printf("issue %s → %s\n", id, status)
	return 0
}

// runIssuesCheck evaluates V1 rules against a live or stored snapshot,
// persists the findings as issues, and prints a human-readable summary.
func runIssuesCheck(args []string) int {
	fs := flag.NewFlagSet("spectra issues check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit findings JSON")
	snapID := fs.String("snapshot", "", "Evaluate against a stored snapshot by ID (default: live)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var snap snapshot.Snapshot
	if *snapID != "" {
		s, err := loadStoredSnapshot(*snapID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "issues check: %v\n", err)
			return 1
		}
		snap = *s
	} else {
		snap = snapshot.Build(context.Background(), snapshot.Options{
			SpectraVersion: version,
		})
		// Persist the snapshot so issues can reference it.
		if err := saveSnapshot(snap); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not persist snapshot: %v\n", err)
		}
	}

	findings := rules.Evaluate(snap, rules.V1Catalog())

	// Record findings as issues.
	db, machineUUID, err := openIssuesDB()
	if err == nil {
		defer db.Close()
		var inputs []store.FindingInput
		for _, f := range findings {
			inputs = append(inputs, store.FindingInput{
				RuleID:   f.RuleID,
				Subject:  f.Subject,
				Severity: string(f.Severity),
				Message:  f.Message,
				Fix:      f.Fix,
			})
		}
		if _, err := db.UpsertIssues(context.Background(), machineUUID, snap.ID, inputs); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not record issues: %v\n", err)
		}
	}

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

// openIssuesDB opens the default DB and returns the machine UUID from the
// most recent snapshot. Returns an error if the DB can't be opened.
func openIssuesDB() (*store.DB, string, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, "", err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, "", err
	}
	// Use machine UUID from the most recent snapshot (or a stable fallback).
	rows, _ := db.ListSnapshots(context.Background(), "")
	machineUUID := "local"
	if len(rows) > 0 {
		machineUUID = rows[0].MachineUUID
	}
	return db, machineUUID, nil
}
