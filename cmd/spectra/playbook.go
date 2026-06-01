package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/playbook"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

type playbookCatalog interface {
	List() []playbook.Playbook
	Get(id string) (playbook.Playbook, bool)
}

func runPlaybook(args []string) int {
	return runPlaybookWith(args, os.Stdout, os.Stderr, playbook.MustDefaultCatalog())
}

func runPlaybookWith(args []string, stdout io.Writer, stderr io.Writer, catalog playbookCatalog) int {
	args, runFlags := extractPlaybookRunFlags(args)
	fs := flag.NewFlagSet("spectra playbook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of human output")
	commandsOnly := fs.Bool("commands", false, "Show only command plan for a playbook")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if runFlags.autoFix && runFlags.nonInteractive && !runFlags.yes {
		fmt.Fprintln(stderr, "--auto-fix cannot be combined with --non-interactive unless --yes is also passed")
		return 2
	}

	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "list" {
		return runPlaybookList(stdout, stderr, catalog, *asJSON)
	}
	id := rest[0]
	pb, ok := catalog.Get(id)
	if !ok {
		fmt.Fprintf(stderr, "unknown playbook %q\n", id)
		printPlaybookUsage(stderr)
		return 2
	}
	if id == "fseventsd-leak" && !*commandsOnly {
		return runFSEventsdLeakPlaybook(stdout, stderr, *asJSON, runFlags)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(pb); err != nil {
			fmt.Fprintf(stderr, "encode playbook: %v\n", err)
			return 1
		}
		return 0
	}
	if *commandsOnly {
		printPlaybookCommands(stdout, pb)
		return 0
	}
	printPlaybook(stdout, pb)
	return 0
}

type playbookRunFlags struct {
	autoFix        bool
	nonInteractive bool
	yes            bool
}

func extractPlaybookRunFlags(args []string) ([]string, playbookRunFlags) {
	out := make([]string, 0, len(args))
	var flags playbookRunFlags
	for _, arg := range args {
		switch arg {
		case "--auto-fix":
			flags.autoFix = true
		case "--non-interactive":
			flags.nonInteractive = true
		case "--yes", "-y":
			flags.yes = true
		default:
			out = append(out, arg)
		}
	}
	return out, flags
}

func runFSEventsdLeakPlaybook(stdout io.Writer, stderr io.Writer, asJSON bool, flags playbookRunFlags) int {
	ctx := context.Background()
	snap := snapshot.Build(ctx, snapshot.Options{
		SpectraVersion: version,
		SkipApps:       true,
		SkipJVMs:       true,
		SkipUpdates:    true,
		StorageOpts: storagestate.CollectOptions{
			IncludeSnapshots: true,
		},
	})
	logs, err := logquery.Run(ctx, logquery.Query{
		Process:  "backupd",
		MinLevel: "Error",
		Last:     24 * time.Hour,
		MaxRows:  50,
	})
	if err != nil {
		fmt.Fprintf(stderr, "warning: backupd log query failed: %v\n", err)
	}
	report := playbook.AnalyzeFSEventsdLeak(snap, logs)
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printFSEventsdLeakReport(stdout, report)
	if flags.autoFix {
		return handleFSEventsdAutoFix(stdout, flags, report)
	}
	return 0
}

func printFSEventsdLeakReport(w io.Writer, report playbook.FSEventsdLeakReport) {
	if report.ExitReason != "" {
		fmt.Fprintln(w, report.ExitReason)
		return
	}
	if report.Matched {
		fmt.Fprintf(w, "=> Match: %s\n", report.RuleID)
	} else {
		fmt.Fprintf(w, "=> No match: %s\n", report.RuleID)
	}
	s := report.Signals
	fmt.Fprintf(w, "   - fseventsd RSS: %s\n", playbook.FormatBytes(s.FSEventsdRSSBytes))
	fmt.Fprintf(w, "   - swap used: %.1f%% of physical memory\n", s.SwapRatio*100)
	fmt.Fprintf(w, "   - TM destinations: %d\n", s.TMDestinationCount)
	fmt.Fprintf(w, "   - backupd-auto: %s\n", loadedString(s.BackupdAutoLoaded))
	fmt.Fprintf(w, "   - MSUPrepareUpdate snapshot: %s\n", yesNo(s.MSUPrepareSnapshot))
	fmt.Fprintf(w, "   - backupd error rate: %s\n", playbook.FormatBackupdErrorRate(s.BackupdErrorCount, 24*time.Hour))
	if len(report.Remediation) > 0 {
		fmt.Fprintln(w, "Remediation:")
		for _, cmd := range report.Remediation {
			fmt.Fprintf(w, "   %s\n", cmd)
		}
	}
}

func handleFSEventsdAutoFix(stdout io.Writer, flags playbookRunFlags, report playbook.FSEventsdLeakReport) int {
	if !report.Matched {
		fmt.Fprintln(stdout, "auto-fix skipped: playbook did not match")
		return 0
	}
	if !flags.yes {
		fmt.Fprint(stdout, "Apply remediation through spectra-helper? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(answer), "y") && !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			fmt.Fprintln(stdout, "auto-fix cancelled")
			return 0
		}
	}
	fmt.Fprintln(stdout, "confirmed remediation commands:")
	for _, cmd := range report.Remediation {
		fmt.Fprintf(stdout, "   %s\n", cmd)
	}
	return 0
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func runPlaybookList(stdout io.Writer, stderr io.Writer, catalog playbookCatalog, asJSON bool) int {
	rows := catalog.List()
	if asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintf(stderr, "encode playbooks: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(stderr, "no playbooks registered")
		return 0
	}
	fmt.Fprintf(stdout, "%-18s  %-22s  %s\n", "ID", "TITLE", "SYMPTOM")
	fmt.Fprintln(stdout, strings.Repeat("-", 78))
	for _, pb := range rows {
		fmt.Fprintf(stdout, "%-18s  %-22s  %s\n", pb.ID, truncate(pb.Title, 22), truncate(pb.Symptom, 34))
	}
	return 0
}

func printPlaybook(w io.Writer, pb playbook.Playbook) {
	fmt.Fprintf(w, "# %s\n\n", pb.Title)
	fmt.Fprintf(w, "id:      %s\n", pb.ID)
	fmt.Fprintf(w, "symptom: %s\n", pb.Symptom)
	if pb.Description != "" {
		fmt.Fprintf(w, "\n%s\n", pb.Description)
	}
	for i, step := range pb.Steps {
		fmt.Fprintf(w, "\n%d. %s\n", i+1, step.Title)
		if step.Purpose != "" {
			fmt.Fprintf(w, "   %s\n", step.Purpose)
		}
		for _, cmd := range step.Commands {
			fmt.Fprintf(w, "   $ spectra %s", strings.Join(cmd.Args, " "))
			if cmd.Remote {
				fmt.Fprint(w, "  # remote")
			}
			if cmd.Destructive {
				fmt.Fprint(w, "  # capture/pausing action")
			}
			if cmd.Description != "" {
				fmt.Fprintf(w, "\n     %s", cmd.Description)
			}
			fmt.Fprintln(w)
		}
		for _, sig := range step.Signals {
			fmt.Fprintf(w, "   - %s: %s\n", sig.Name, sig.Meaning)
		}
	}
	if len(pb.References) > 0 {
		fmt.Fprintln(w, "\nReferences:")
		for _, ref := range pb.References {
			fmt.Fprintf(w, "  - %s: %s\n", ref.Title, ref.Path)
		}
	}
}

func printPlaybookCommands(w io.Writer, pb playbook.Playbook) {
	for _, step := range pb.Steps {
		for _, cmd := range step.Commands {
			fmt.Fprintf(w, "spectra %s\n", strings.Join(cmd.Args, " "))
		}
	}
}

func printPlaybookUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: spectra playbook [--json] [list|<id>]")
	fmt.Fprintln(w, "   or: spectra playbook --commands <id>")
	fmt.Fprintln(w, "   or: spectra playbook fseventsd-leak [--auto-fix] [--non-interactive --yes]")
}
