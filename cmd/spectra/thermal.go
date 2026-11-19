package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/kaeawc/spectra/internal/thermal"
)

func runThermal(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runThermalWithIO(ctx, args, os.Stdout, os.Stderr, &thermal.Collector{})
}

func thermalExit(v thermal.Verdict) int {
	switch v {
	case thermal.Throttled:
		return thermal.ExitThrottled
	case thermal.Warm:
		return thermal.ExitWarm
	}
	return thermal.ExitNominal
}

func runThermalWithIO(ctx context.Context, args []string, stdout, stderr io.Writer, c *thermal.Collector) int {
	fs := flag.NewFlagSet("spectra thermal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: spectra thermal [--json] [--watch <duration> [--interval <duration>]]

Reports whether the host is thermally throttled (macOS: pmset -g therm;
Linux: /sys thermal zones, cooling devices, cpufreq, thermal_throttle).

Exit codes: 0 nominal, 3 warm, 4 throttled, 1 error, 2 usage.

`)
		fs.PrintDefaults()
	}
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	watch := fs.Duration("watch", 0, "Sample for this long and report the worst verdict and any throttle-counter increase")
	interval := fs.Duration("interval", thermal.DefaultWatchInterval, "Sampling interval for --watch")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *watch < 0 || *interval <= 0 {
		fmt.Fprintln(stderr, "spectra thermal: invalid arguments (see --help)")
		return 2
	}

	var rep thermal.WatchReport
	var err error
	if *watch > 0 {
		rep, err = c.Watch(ctx, *watch, *interval)
	} else {
		var r thermal.Report
		r, err = c.Collect(ctx)
		rep = thermal.WatchReport{Report: r, Samples: 1}
	}
	if err != nil {
		fmt.Fprintf(stderr, "spectra thermal: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "spectra thermal: %v\n", err)
			return 1
		}
	} else {
		if err := printThermal(stdout, rep, *watch > 0); err != nil {
			fmt.Fprintf(stderr, "thermal: write output: %v\n", err)
			return 1
		}
	}
	return thermalExit(rep.Verdict)
}

func printThermal(out io.Writer, r thermal.WatchReport, watched bool) error {
	w := &errWriter{w: out}
	fmt.Fprintf(w, "thermal: %s — %s\n", r.Verdict, r.Evidence)
	if watched {
		fmt.Fprintf(w, "  worst of %d samples\n", r.Samples)
		for _, inc := range r.CounterIncreases {
			fmt.Fprintf(w, "  throttle counter %s: %d -> %d\n", inc.Path, inc.From, inc.To)
		}
	}
	for _, f := range r.Findings {
		if f.Evidence != r.Evidence {
			fmt.Fprintf(w, "  also [%s] %s\n", f.Verdict, f.Evidence)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
	return w.err
}
