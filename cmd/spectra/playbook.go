package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/playbook"
)

type playbookCatalog interface {
	List() []playbook.Playbook
	Get(id string) (playbook.Playbook, bool)
}

func runPlaybook(args []string) int {
	return runPlaybookWith(args, os.Stdout, os.Stderr, playbook.MustDefaultCatalog())
}

func runPlaybookWith(args []string, stdout io.Writer, stderr io.Writer, catalog playbookCatalog) int {
	fs := flag.NewFlagSet("spectra playbook", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of human output")
	commandsOnly := fs.Bool("commands", false, "Show only command plan for a playbook")
	if err := fs.Parse(args); err != nil {
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
}
