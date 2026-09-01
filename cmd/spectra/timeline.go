package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/timeline"
)

// processLister returns the currently running processes; injected for testing.
type processLister func() []process.Info

func runTimeline(args []string) int {
	return runTimelineWithIO(args, os.Stdout, os.Stderr, time.Now, defaultProcessLister)
}

func runTimelineWithIO(args []string, stdout, stderr io.Writer, now func() time.Time, procs processLister) int {
	fs := flag.NewFlagSet("spectra timeline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	since := fs.Duration("since", time.Hour, "Only show events within this window (e.g. 30m, 2h)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a text timeline")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *since <= 0 {
		fmt.Fprintln(stderr, "timeline: --since must be positive")
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: spectra timeline [--since <dur>] [--json]")
		return 2
	}

	cutoff := now().Add(-*since)
	tl, errs := timeline.Collect(cutoff, processStartSource{procs: procs})
	for _, e := range errs {
		fmt.Fprintf(stderr, "timeline: %v\n", e)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(tl); err != nil {
			fmt.Fprintf(stderr, "timeline: write output: %v\n", err)
			return 1
		}
		return 0
	}
	if err := tl.Render(stdout); err != nil {
		fmt.Fprintf(stderr, "timeline: write output: %v\n", err)
		return 1
	}
	return 0
}

// processStartSource turns each running process's start time into an event.
type processStartSource struct {
	procs processLister
}

func (processStartSource) Name() string { return "process" }

func (s processStartSource) Events(since time.Time) ([]timeline.Event, error) {
	var out []timeline.Event
	for _, p := range s.procs() {
		if p.StartTime.IsZero() || p.StartTime.Before(since) {
			continue
		}
		name := p.Command
		if name == "" {
			name = p.BSDName
		}
		out = append(out, timeline.Event{
			Time:     p.StartTime,
			Source:   "process",
			Severity: timeline.SeverityInfo,
			Summary:  fmt.Sprintf("%s [%d] started", name, p.PID),
		})
	}
	return out, nil
}

func defaultProcessLister() []process.Info {
	return process.CollectAll(context.Background(), process.CollectOptions{})
}
