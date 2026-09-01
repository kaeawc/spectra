package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/timeline"
	"github.com/kaeawc/spectra/internal/updates"
)

// processLister returns the currently running processes; injected for testing.
type processLister func() []process.Info

func runTimeline(args []string) int {
	sources := []timeline.Source{
		processStartSource{procs: defaultProcessLister},
		logSource{run: logquery.Run},
		installSource{fetch: defaultInstallFetch},
	}
	return runTimelineWithIO(args, os.Stdout, os.Stderr, time.Now, sources...)
}

func runTimelineWithIO(args []string, stdout, stderr io.Writer, now func() time.Time, sources ...timeline.Source) int {
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
	tl, errs := timeline.Collect(cutoff, sources...)
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

// --- process-start source ---------------------------------------------------

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

// --- unified-log source -----------------------------------------------------

type logSource struct {
	run func(ctx context.Context, q logquery.Query) (logquery.Result, error)
}

func (logSource) Name() string { return "log" }

func (s logSource) Events(since time.Time) ([]timeline.Event, error) {
	res, err := s.run(context.Background(), logquery.Query{
		Start:           since,
		MinLevel:        "error",
		AllowLongWindow: true,
	})
	if err != nil {
		return nil, err
	}
	out := make([]timeline.Event, 0, len(res.Entries))
	for _, e := range res.Entries {
		msg := e.EventMessage
		if e.Process != "" {
			msg = e.Process + ": " + msg
		}
		out = append(out, timeline.Event{
			Time:     e.Timestamp,
			Source:   "log",
			Severity: logSeverity(e),
			Summary:  msg,
		})
	}
	return out, nil
}

func logSeverity(e logquery.LogEntry) timeline.Severity {
	kind := strings.ToLower(e.MessageType)
	if kind == "" {
		kind = strings.ToLower(e.LogType)
	}
	// Exact match: "default" contains "fault" as a substring, so Contains is wrong.
	if kind == "fault" || kind == "error" {
		return timeline.SeverityError
	}
	return timeline.SeverityWarn
}

// --- install-history source -------------------------------------------------

type installFetch func(ctx context.Context) ([]updates.InstallEntry, error)

type installSource struct {
	fetch installFetch
}

func (installSource) Name() string { return "update" }

func (s installSource) Events(since time.Time) ([]timeline.Event, error) {
	entries, err := s.fetch(context.Background())
	if err != nil {
		return nil, err
	}
	var out []timeline.Event
	for _, e := range entries {
		if e.InstallDate.IsZero() || e.InstallDate.Before(since) {
			continue
		}
		summary := "installed " + e.Name
		if e.Version != "" {
			summary += " " + e.Version
		}
		out = append(out, timeline.Event{
			Time:     e.InstallDate,
			Source:   "update",
			Severity: timeline.SeverityInfo,
			Summary:  summary,
		})
	}
	return out, nil
}

// --- default (production) dependencies --------------------------------------

func defaultProcessLister() []process.Info {
	return process.CollectAll(context.Background(), process.CollectOptions{})
}

func defaultInstallFetch(ctx context.Context) ([]updates.InstallEntry, error) {
	history, err := updates.Collect(ctx)
	if err != nil {
		return nil, err
	}
	return history.Entries, nil
}
