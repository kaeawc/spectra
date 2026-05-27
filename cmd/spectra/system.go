package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/syslimits"
)

func systemSubcommands() []subcommand {
	return []subcommand{
		{"limits", "Show pty/file/process kernel limits vs current usage", runSystemLimits},
	}
}

func runSystem(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		for _, sc := range systemSubcommands() {
			if args[0] == sc.name {
				return sc.run(args[1:])
			}
		}
	}
	fmt.Fprintln(os.Stderr, "usage: spectra system limits [--json] [--top]")
	return 2
}

func runSystemLimits(args []string) int {
	fs := flag.NewFlagSet("spectra system limits", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	showTop := fs.Bool("top", false, "Show top process holders for saturated resources")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	limits := syslimits.Collect(syslimits.Options{})
	var top syslimits.TopHolders
	if *showTop {
		top = syslimits.CollectTopHolders(context.Background(), 10, processCollectOptions())
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if *showTop {
			_ = enc.Encode(struct {
				Limits     syslimits.SystemLimits `json:"limits"`
				TopHolders syslimits.TopHolders   `json:"top_holders,omitempty"`
			}{Limits: limits, TopHolders: top})
		} else {
			_ = enc.Encode(limits)
		}
		return criticalExit(limits)
	}

	printSystemLimits(limits)
	if *showTop {
		printSystemLimitTop(limits, top)
	}
	return criticalExit(limits)
}

func processCollectOptions() process.CollectOptions {
	return process.CollectOptions{}
}

func criticalExit(limits syslimits.SystemLimits) int {
	if limits.AnyCritical() {
		return 1
	}
	return 0
}

func printSystemLimits(limits syslimits.SystemLimits) {
	fmt.Printf("%-14s  %-8s  %-8s  %-7s  %s\n", "RESOURCE", "USED", "LIMIT", "PCT", "STATE")
	fmt.Println(strings.Repeat("-", 52))
	printUsageRow("pty slots", limits.PTY)
	printUsageRow("open files", limits.Files)
	printUsageRow("processes", limits.Procs)
	printUsageRow("processes/uid", limits.ProcsPerUID)
	fmt.Printf("%-14s  %-8s  %-8d  %-7s  %s\n", "files/proc", "-", limits.FilesPerProc, "-", "")
	if len(limits.PartialFailures) > 0 {
		fmt.Fprintf(os.Stderr, "warning: partial collection failures: %s\n", strings.Join(limits.PartialFailures, ", "))
	}
}

func printUsageRow(label string, usage syslimits.ResourceUsage) {
	state := ""
	switch {
	case usage.Critical:
		state = "CRITICAL"
	case usage.Warn:
		state = "WARN"
	}
	pct := "-"
	if usage.Limit > 0 {
		pct = fmt.Sprintf("%.0f%%", usage.Pct)
	}
	fmt.Printf("%-14s  %-8d  %-8d  %-7s  %s\n", label, usage.Current, usage.Limit, pct, state)
}

func printSystemLimitTop(limits syslimits.SystemLimits, top syslimits.TopHolders) {
	printTopForResource("pty slots", "pty", limits.PTY, top)
	printTopForResource("open files", "files", limits.Files, top)
	printTopForResource("processes", "procs", limits.Procs, top)
	printTopForResource("processes/uid", "procs_per_uid", limits.ProcsPerUID, top)
}

func printTopForResource(label, key string, usage syslimits.ResourceUsage, top syslimits.TopHolders) {
	if !usage.Warn && !usage.Critical {
		return
	}
	rows := top[key]
	if len(rows) == 0 {
		return
	}
	state := "WARN"
	if usage.Critical {
		state = "CRITICAL"
	}
	fmt.Printf("\nRESOURCE: %s (%d/%d, %s)\n", label, usage.Current, usage.Limit, state)
	fmt.Printf("  %-7s  %-7s  %s\n", "PID", "COUNT", "COMMAND")
	for _, row := range rows {
		fmt.Printf("  %-7d  %-7d  %s\n", row.PID, row.Count, truncate(row.Command, 48))
	}
}
