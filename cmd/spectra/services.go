package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/services"
)

func runServices(args []string) int {
	return runServicesWithIO(args, os.Stdout, os.Stderr, services.Options{})
}

// runServicesWithIO lists jobs on top of base, which supplies the runner and
// plist directories in tests.
func runServicesWithIO(args []string, stdout, stderr io.Writer, base services.Options) int {
	fs := flag.NewFlagSet("spectra services", flag.ContinueOnError)
	fs.SetOutput(stderr)
	domain := fs.String("domain", "system", "Domain: system, user, or all")
	label := fs.String("label", "", "Filter labels by substring")
	running := fs.Bool("running", false, "Only show running jobs")
	onDemand := fs.Bool("on-demand", false, "Only show on-demand jobs")
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := base
	opts.Domain = *domain
	inv, err := services.Report(context.Background(), opts, services.Filter{Label: *label, Running: *running, OnDemand: *onDemand})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(inv)
		return 0
	}
	printServices(stdout, inv)
	return 0
}

func printServices(w io.Writer, inv services.LaunchInventory) {
	fmt.Fprintln(w, "=== Services ===")
	fmt.Fprintf(w, "jobs: %d\n", len(inv.Jobs))
	fmt.Fprintf(w, "  %-48s  %-8s  %7s  %s\n", "LABEL", "DOMAIN", "PID", "PLIST")
	fmt.Fprintln(w, "  "+strings.Repeat("-", 88))
	for _, job := range inv.Jobs {
		pid := "-"
		if job.PID > 0 {
			pid = fmt.Sprint(job.PID)
		}
		fmt.Fprintf(w, "  %-48s  %-8s  %7s  %s\n",
			truncate(job.Label, 48),
			job.Domain,
			pid,
			job.PlistPath,
		)
	}
}
