package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/crashquery"
	"github.com/kaeawc/spectra/internal/hostos"
)

// defaultDiagnosticReportsDir is where macOS collects .ips reports, or "" when
// there is none: off macOS, or when the home directory cannot be resolved.
func defaultDiagnosticReportsDir() string {
	home, _ := os.UserHomeDir()
	return crashquery.DefaultDir(hostos.Current(), home)
}

// requireCrashDir reports, and returns false, when no reports directory is
// known. A missing directory used to read as "0 reports", which on Linux
// (no DiagnosticReports) looked like a clean machine.
func requireCrashDir(cmd, dir string, stderr io.Writer) bool {
	if dir != "" {
		return true
	}
	if hostos.Current() != hostos.Darwin {
		fmt.Fprintf(stderr, "spectra %s: %v; pass --dir with a directory of .ips reports\n", cmd, hostos.Unsupported(crashquery.ReportsFeature))
		return false
	}
	fmt.Fprintf(stderr, "spectra %s: cannot resolve the home directory; pass --dir\n", cmd)
	return false
}

func runCrashList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", crashquery.DefaultLimit, "maximum reports to show")
	dir := fs.String("dir", defaultDiagnosticReportsDir(), "DiagnosticReports directory")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash list [--json] [--limit N] [--dir <path>]")
		fmt.Fprintln(stderr, "List the .ips crash reports macOS has collected, newest first.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if !requireCrashDir("crash list", *dir, stderr) {
		return 1
	}
	return runCrashListWithDeps(*asJSON, *limit, stdout, stderr, crashquery.DirSource(*dir))
}

func runCrashListWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, src crashquery.Source) int {
	res, err := crashquery.List(context.Background(), src, limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return encodeJSON(stdout, stderr, res.Summaries)
	}
	renderCrashList(stdout, res.Summaries, res.Total, res.Skipped)
	return 0
}

func renderCrashList(w io.Writer, summaries []crashquery.Summary, total, skipped int) {
	if total == 0 {
		fmt.Fprintf(w, "No crash reports found (%d skipped).\n", skipped)
		return
	}
	fmt.Fprintf(w, "Crash reports (%d shown of %d", len(summaries), total)
	if skipped > 0 {
		fmt.Fprintf(w, ", %d skipped", skipped)
	}
	fmt.Fprintln(w, "):")
	for _, s := range summaries {
		when := s.Time
		if !s.At.IsZero() {
			when = s.At.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-16s  %-28s %-8s %s\n", when, truncate(s.Process, 28), s.Kind, s.Exception)
		if detail := crashListDetail(s); detail != "" {
			fmt.Fprintf(w, "      %s\n", detail)
		}
	}
}

// crashListDetail is the second line under an entry: the incident id and the
// source file (which the user can pass to `spectra crash inspect`).
func crashListDetail(s crashquery.Summary) string {
	detail := ""
	if s.Incident != "" {
		detail = "incident " + s.Incident
	}
	if s.File != "" {
		if detail != "" {
			detail += " · "
		}
		detail += s.File
	}
	return detail
}
