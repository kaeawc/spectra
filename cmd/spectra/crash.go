package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/crashready"
	"github.com/kaeawc/spectra/internal/detect"
)

func runCrash(args []string) int {
	return runCrashWithIO(args, os.Stdout, os.Stderr, detect.Detect)
}

func runCrashWithIO(args []string, stdout, stderr io.Writer, inspect func(string) (detect.Result, error)) int {
	sub := "readiness"
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	if sub != "readiness" {
		fmt.Fprintf(stderr, "unknown crash subcommand %q (want: readiness)\n", sub)
		return 2
	}
	return runCrashReadiness(rest, stdout, stderr, inspect)
}

func runCrashReadiness(args []string, stdout, stderr io.Writer, inspect func(string) (detect.Result, error)) int {
	fs := flag.NewFlagSet("crash readiness", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash readiness [--json] [/path/to/App.app]")
		fmt.Fprintln(stderr, "Audit whether this machine (and optionally one app) can produce a debuggable crash.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var app *crashready.AppDebug
	if p := fs.Arg(0); p != "" {
		res, err := inspect(p)
		if err != nil {
			fmt.Fprintf(stderr, "inspect %s: %v\n", p, err)
			return 1
		}
		app = &crashready.AppDebug{
			Name:            strings.TrimSuffix(filepath.Base(res.Path), ".app"),
			HardenedRuntime: res.HardenedRuntime,
			GetTaskAllow:    hasEntitlement(res.Entitlements, "get-task-allow"),
		}
	}

	report := crashready.Evaluate(crashready.NewLiveHost(), app)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderCrashReadiness(stdout, report)
	return 0
}

func hasEntitlement(ents []string, want string) bool {
	for _, e := range ents {
		if e == want {
			return true
		}
	}
	return false
}

func crashStatusLabel(s crashready.Status) string {
	switch s {
	case crashready.StatusCritical:
		return "CRIT"
	case crashready.StatusWarn:
		return "warn"
	default:
		return "ok"
	}
}

func renderCrashReadiness(w io.Writer, r crashready.Report) {
	header := "Post-mortem readiness"
	if r.App != "" {
		header += " — " + r.App
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, strings.Repeat("-", len(header)))
	for _, c := range r.Checks {
		fmt.Fprintf(w, "[%-4s] %s\n", crashStatusLabel(c.Status), c.Name)
		fmt.Fprintf(w, "        %s\n", c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "        fix: %s\n", c.Fix)
		}
	}
	warn, crit := r.Counts()
	verdict := "ready: a crash here should leave debuggable evidence"
	if crit > 0 {
		verdict = "NOT ready: a crash here may leave nothing debuggable"
	}
	fmt.Fprintf(w, "\n%s (%d warning(s), %d critical)\n", verdict, warn, crit)
}
