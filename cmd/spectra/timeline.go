package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/timeline"
)

func runTimeline(args []string) int {
	sources, note := timelineSources(hostos.Current())
	if note != "" {
		fmt.Fprintf(os.Stderr, "timeline: %s\n", note)
	}
	return runTimelineWithIO(args, os.Stdout, os.Stderr, time.Now, sources...)
}

// timelineSources returns the production sources for kind.
func timelineSources(kind hostos.Kind) ([]timeline.Source, string) {
	return timeline.HostSources(kind, process.CollectOptions{})
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

	tl, errs := timeline.Report(context.Background(), *since, now(), sources...)
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
