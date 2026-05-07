package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	issueflow "github.com/kaeawc/spectra/internal/issues"
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
		{"record-fix", "Record a fix attempt for an issue", runIssuesRecordFix},
		{"fix-history", "List recorded fix attempts for an issue", runIssuesFixHistory},
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

func runIssuesRecordFix(args []string) int {
	fs := flag.NewFlagSet("spectra issues record-fix", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	appliedBy := fs.String("applied-by", "", "Actor that applied the fix")
	command := fs.String("command", "", "Command or action taken")
	output := fs.String("output", "", "Command output or result note")
	exitCode := fs.Int("exit-code", 0, "Command exit code")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra issues record-fix [--applied-by user] [--command cmd] [--output text] [--exit-code code] <issue-id>")
		return 2
	}

	db, _, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	id, err := db.RecordAppliedFix(context.Background(), store.AppliedFixInput{
		IssueID:   fs.Arg(0),
		AppliedBy: *appliedBy,
		Command:   *command,
		Output:    *output,
		ExitCode:  *exitCode,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "record fix for %q: %v\n", fs.Arg(0), err)
		return 1
	}
	fmt.Printf("recorded fix %s for issue %s\n", id, fs.Arg(0))
	return 0
}

func runIssuesFixHistory(args []string) int {
	fs := flag.NewFlagSet("spectra issues fix-history", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra issues fix-history [--json] <issue-id>")
		return 2
	}

	db, _, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	rows, err := db.ListAppliedFixes(context.Background(), fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fix history for %q: %v\n", fs.Arg(0), err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Printf("no fix history for issue %s\n", fs.Arg(0))
		return 0
	}
	fmt.Printf("%-20s  %-20s  %-12s  %-8s  %s\n", "ID", "APPLIED_AT", "APPLIED_BY", "EXIT", "COMMAND")
	fmt.Println(strings.Repeat("-", 90))
	for _, r := range rows {
		fmt.Printf("%-20s  %-20s  %-12s  %-8d  %s\n",
			truncate(r.ID, 20), r.AppliedAt.Format(time.RFC3339),
			truncate(r.AppliedBy, 12), r.ExitCode, truncate(r.Command, 60),
		)
	}
	fmt.Printf("\n%d fix attempt(s)\n", len(rows))
	return 0
}

// runIssuesCheck evaluates V1 rules against a live or stored snapshot,
// persists the findings as issues, and prints a human-readable summary.
func runIssuesCheck(args []string) int {
	fs := flag.NewFlagSet("spectra issues check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit findings JSON")
	snapID := fs.String("snapshot", "", "Evaluate against a stored snapshot by ID (default: live)")
	rulesConfig := fs.String("rules-config", "", "Path to spectra.yml rule overrides (default: ./spectra.yml if present)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	catalog, err := loadRuleCatalog(*rulesConfig, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "issues check: %v\n", err)
		return 1
	}
	db, _, err := openIssuesDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	svc := issueflow.Service{
		Store:         db,
		SnapshotStore: cliSnapshotStore{db: db},
		Snapshots:     cliSnapshotSource{version: version},
		Engine: issueflow.EngineFunc(func(s snapshot.Snapshot) []rules.Finding {
			return rules.Evaluate(s, catalog)
		}),
	}
	result, err := svc.Check(context.Background(), issueflow.CheckOptions{SnapshotID: *snapID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "issues check: %v\n", err)
		return 1
	}
	findings := result.Findings

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

type cliSnapshotSource struct {
	version string
}

func (s cliSnapshotSource) Live(ctx context.Context) (snapshot.Snapshot, error) {
	return snapshot.Build(ctx, snapshot.Options{
		SpectraVersion: s.version,
		DetectOpts:     detect.Options{},
	}), nil
}

func (cliSnapshotSource) Stored(_ context.Context, id string) (snapshot.Snapshot, error) {
	s, err := loadStoredSnapshot(id)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	return *s, nil
}

type cliSnapshotStore struct {
	db *store.DB
}

func (s cliSnapshotStore) SaveSnapshot(ctx context.Context, snap store.SnapshotInput) error {
	return s.db.SaveSnapshot(ctx, snap)
}

func (s cliSnapshotStore) SaveSnapshotProcesses(ctx context.Context, snapshotID string, processes []store.ProcessSnapshotRow) error {
	return s.db.SaveSnapshotProcesses(ctx, snapshotID, processes)
}

func (s cliSnapshotStore) SaveLoginItems(ctx context.Context, snapshotID string, items []store.LoginItemRow) error {
	return s.db.SaveLoginItems(ctx, snapshotID, items)
}

func (s cliSnapshotStore) SaveGrantedPerms(ctx context.Context, snapshotID string, perms []store.GrantedPermRow) error {
	return s.db.SaveGrantedPerms(ctx, snapshotID, perms)
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
