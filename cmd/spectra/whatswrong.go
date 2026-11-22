package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/watchclient"
	"github.com/kaeawc/spectra/internal/whatswrong"
)

func defaultWhatswrongDeps() whatswrong.Deps {
	deps := whatswrong.DefaultDeps()
	deps.Connect = func(ctx context.Context) (whatswrong.WatchClient, bool) {
		paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
		if err != nil {
			return nil, false
		}
		return watchclient.Connect(ctx, os.Getenv, paths)
	}
	return deps
}

func runWhatswrong(args []string) int {
	return runWhatswrongWithIO(args, os.Stdout, os.Stderr, defaultWhatswrongDeps())
}

func runWhatswrongWithIO(args []string, stdout, stderr io.Writer, deps whatswrong.Deps) int {
	fs := flag.NewFlagSet("whatswrong", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	noDaemon := fs.Bool("no-daemon", false, "disable daemon enrichment")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *noDaemon {
		deps.Connect = nil
	}
	if deps.Logger == nil {
		deps.Logger = cliLogger
	}
	report := whatswrong.Collect(context.Background(), deps)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	whatswrong.Render(stdout, report.Causes)
	if report.Trend != "" {
		fmt.Fprintln(stdout, "Trend:", report.Trend)
	}
	return 0
}
