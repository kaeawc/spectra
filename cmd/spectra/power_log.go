package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/kaeawc/spectra/internal/powerlog"
)

func runPowerLog(args []string, stdout, stderr io.Writer, run powerlog.Runner) int {
	fs := flag.NewFlagSet("power log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("n", 20, "show at most N most-recent sleep/wake events")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra power log [--json] [-n N]")
		fmt.Fprintln(stderr, "Show recent sleep/wake history and what is currently keeping this Mac awake.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	report, err := powerlog.Collect(run)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderPowerLog(stdout, report, *limit)
	return 0
}

func renderPowerLog(w io.Writer, r *powerlog.Report, limit int) {
	events := r.Events
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	fmt.Fprintf(w, "Sleep/Wake events (showing %d of %d):\n", len(events), len(r.Events))
	for _, e := range events {
		fmt.Fprintf(w, "  %s  %-9s %s\n", e.RawTime, e.Type, e.Detail)
	}
	fmt.Fprintln(w, "")
	if len(r.SleepBlockers) == 0 {
		fmt.Fprintln(w, "Nothing is currently blocking sleep.")
		return
	}
	fmt.Fprintln(w, "Currently blocking sleep:")
	for _, b := range r.SleepBlockers {
		fmt.Fprintf(w, "  %s (pid %d) %s, held %s — %q\n", b.Process, b.PID, b.Type, b.Held, b.Reason)
	}
}
