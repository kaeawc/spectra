package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/crashready"
	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/detect"
)

func runCrash(args []string) int {
	return runCrashWithIO(args, os.Stdout, os.Stderr, detect.Detect)
}

func runCrashWithIO(args []string, stdout, stderr io.Writer, inspect func(string) (detect.Result, error)) int {
	sub := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	switch sub {
	case "", "readiness":
		return runCrashReadiness(rest, stdout, stderr, inspect)
	case "inspect":
		return runCrashInspect(rest, stdout, stderr, os.ReadFile)
	case "resource":
		return runCrashResource(rest, stdout, stderr, os.ReadFile)
	case "list":
		return runCrashList(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown crash subcommand %q (want: readiness, inspect, resource, list)\n", sub)
		return 2
	}
}

func runCrashResource(args []string, stdout, stderr io.Writer, readFile func(string) ([]byte, error)) int {
	fs := flag.NewFlagSet("crash resource", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash resource [--json] <report.ips>")
		fmt.Fprintln(stderr, "Decode an EXC_RESOURCE / watchdog resource-limit kill (CPU, wakeups, I/O, memory).")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	path := fs.Arg(0)
	report, code := loadCrashReport(path, readFile, stderr)
	if code != 0 {
		return code
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
	renderCrashResource(stdout, report)
	return 0
}

func renderCrashResource(w io.Writer, r *crashreport.Report) {
	if r.Resource == nil {
		fmt.Fprintf(w, "%s is not a resource-limit kill", r.Process)
		if r.Exception != "" {
			fmt.Fprintf(w, " (exception %s)", r.Exception)
		}
		fmt.Fprintln(w, ".")
		fmt.Fprintln(w, "Use `spectra crash inspect` for a full decode.")
		return
	}
	res := r.Resource
	fmt.Fprintf(w, "%s — resource-limit kill: %s\n", r.Process, res.Flavor)
	fmt.Fprintf(w, "  %s\n", res.Explanation)
	if res.Limit != "" || res.Observed != "" || res.Window != "" {
		fmt.Fprintf(w, "  limit=%s  observed=%s  window=%s\n", orDash(res.Limit), orDash(res.Observed), orDash(res.Window))
	}
	if res.Detail != "" {
		fmt.Fprintf(w, "  detail: %s\n", res.Detail)
	}
	for _, t := range r.Threads {
		if !t.Triggered {
			continue
		}
		fmt.Fprintf(w, "\noffending thread %d", t.Index)
		if t.Queue != "" {
			fmt.Fprintf(w, " (%s)", t.Queue)
		}
		fmt.Fprintln(w)
		for i, f := range t.Frames {
			fmt.Fprintf(w, "  %2d  %s\n", i, f)
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func runCrashInspect(args []string, stdout, stderr io.Writer, readFile func(string) ([]byte, error)) int {
	fs := flag.NewFlagSet("crash inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	all := fs.Bool("all", false, "show every thread (default: the faulting thread only)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash inspect [--json] [--all] <report.ips>")
		fmt.Fprintln(stderr, "Decode a macOS .ips crash report into a readable summary.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	report, code := loadCrashReport(fs.Arg(0), readFile, stderr)
	if code != 0 {
		return code
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
	renderCrashReport(stdout, report, *all)
	return 0
}

// loadCrashReport reads and parses one .ips report, mapping failures to a
// process exit code (0 on success). readFile is injected for testability.
func loadCrashReport(path string, readFile func(string) ([]byte, error), stderr io.Writer) (*crashreport.Report, int) {
	data, err := readFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "read %s: %v\n", path, err)
		return nil, 1
	}
	report, err := crashreport.Parse(data)
	if err != nil {
		if errors.Is(err, crashreport.ErrLegacyFormat) {
			fmt.Fprintf(stderr, "%s is a legacy plain-text crash report; spectra decodes modern .ips JSON reports.\n", path)
			return nil, 1
		}
		fmt.Fprintf(stderr, "parse %s: %v\n", path, err)
		return nil, 1
	}
	return report, 0
}

func renderCrashReport(w io.Writer, r *crashreport.Report, allThreads bool) {
	renderCrashHeader(w, r)
	renderCrashThreads(w, r, allThreads)
}

func renderCrashHeader(w io.Writer, r *crashreport.Report) {
	fmt.Fprintf(w, "%s", r.Process)
	if r.Version != "" {
		fmt.Fprintf(w, " %s", r.Version)
	}
	fmt.Fprintf(w, "  [%s]\n", r.Kind)
	if r.OSVersion != "" {
		fmt.Fprintf(w, "  os:        %s\n", r.OSVersion)
	}
	if r.Time != "" {
		fmt.Fprintf(w, "  time:      %s\n", r.Time)
	}
	if r.Exception != "" {
		fmt.Fprintf(w, "  exception: %s", r.Exception)
		if r.ExceptionDetail != "" {
			fmt.Fprintf(w, " — %s", r.ExceptionDetail)
		}
		fmt.Fprintln(w)
	}
	if r.Termination != "" {
		fmt.Fprintf(w, "  reason:    %s\n", r.Termination)
	}
	if r.Codes != "" {
		fmt.Fprintf(w, "  codes:     %s\n", r.Codes)
	}
}

func renderCrashThreads(w io.Writer, r *crashreport.Report, allThreads bool) {
	for _, t := range r.Threads {
		if !allThreads && !t.Triggered {
			continue
		}
		label := fmt.Sprintf("Thread %d", t.Index)
		if t.Queue != "" {
			label += " (" + t.Queue + ")"
		}
		if t.Triggered {
			label += "  <-- crashed"
		}
		fmt.Fprintf(w, "\n%s\n", label)
		for i, f := range t.Frames {
			fmt.Fprintf(w, "  %2d  %s\n", i, f)
		}
	}
	if !allThreads && len(r.Threads) > 1 {
		fmt.Fprintf(w, "\n(%d threads total; pass --all to show them)\n", len(r.Threads))
	}
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
