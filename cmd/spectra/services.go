package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/services"
)

func runServices(args []string) int {
	fs := flag.NewFlagSet("spectra services", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	domain := fs.String("domain", "system", "Domain: system, user, or all")
	label := fs.String("label", "", "Filter labels by substring")
	running := fs.Bool("running", false, "Only show running jobs")
	onDemand := fs.Bool("on-demand", false, "Only show on-demand jobs")
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	inv, err := services.List(context.Background(), services.Options{Domain: *domain})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	inv.Jobs = filterServiceJobs(inv.Jobs, *label, *running, *onDemand)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(inv)
		return 0
	}
	printServices(inv)
	return 0
}

func filterServiceJobs(jobs []services.LaunchJob, label string, running, onDemand bool) []services.LaunchJob {
	if label == "" && !running && !onDemand {
		return jobs
	}
	label = strings.ToLower(label)
	out := make([]services.LaunchJob, 0, len(jobs))
	for _, job := range jobs {
		if label != "" && !strings.Contains(strings.ToLower(job.Label), label) {
			continue
		}
		if running && job.PID == 0 {
			continue
		}
		if onDemand && !job.OnDemand {
			continue
		}
		out = append(out, job)
	}
	return out
}

func printServices(inv services.LaunchInventory) {
	fmt.Println("=== Services ===")
	fmt.Printf("jobs: %d\n", len(inv.Jobs))
	fmt.Printf("  %-48s  %-8s  %7s  %s\n", "LABEL", "DOMAIN", "PID", "PLIST")
	fmt.Println("  " + strings.Repeat("-", 88))
	for _, job := range inv.Jobs {
		pid := "-"
		if job.PID > 0 {
			pid = fmt.Sprint(job.PID)
		}
		fmt.Printf("  %-48s  %-8s  %7s  %s\n",
			truncate(job.Label, 48),
			job.Domain,
			pid,
			job.PlistPath,
		)
	}
}
