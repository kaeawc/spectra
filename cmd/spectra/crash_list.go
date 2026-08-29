package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
)

// ipsTimeLayout is the timestamp format in an .ips header, e.g.
// "2026-08-05 23:17:34.00 -0500".
const ipsTimeLayout = "2006-01-02 15:04:05.00 -0700"

// sweptReport pairs a parsed crash report with its source file. It is the
// reusable unit the crash-inventory, signature, and jetsam commands share.
type sweptReport struct {
	File   string
	Report *crashreport.Report
}

// sweepCrashReports parses each file, skipping legacy/unreadable/unparseable
// ones (returned as the skipped count) so one bad report never breaks a sweep.
func sweepCrashReports(files []string, read func(string) ([]byte, error)) ([]sweptReport, int) {
	var out []sweptReport
	skipped := 0
	for _, f := range files {
		data, err := read(f)
		if err != nil {
			skipped++
			continue
		}
		rep, err := crashreport.Parse(data)
		if err != nil {
			skipped++
			continue
		}
		out = append(out, sweptReport{File: f, Report: rep})
	}
	return out, skipped
}

type crashSummary struct {
	Time      string    `json:"time"`
	Process   string    `json:"process"`
	Kind      string    `json:"kind"`
	Exception string    `json:"exception,omitempty"`
	Incident  string    `json:"incident_id,omitempty"`
	File      string    `json:"file"`
	at        time.Time // parsed, for sorting only
}

func summarize(s sweptReport) crashSummary {
	r := s.Report
	cs := crashSummary{
		Time: r.Time, Process: r.Process, Kind: r.Kind,
		Exception: r.Exception, Incident: r.IncidentID, File: s.File,
	}
	if t, err := time.Parse(ipsTimeLayout, r.Time); err == nil {
		cs.at = t
	}
	return cs
}

type crashListDeps struct {
	list func() ([]string, error)
	read func(string) ([]byte, error)
}

func defaultCrashListDeps(dir string) crashListDeps {
	return crashListDeps{
		list: func() ([]string, error) { return findIPSFiles(dir) },
		read: os.ReadFile,
	}
}

// findIPSFiles walks dir (including Retired/ and dotfile reports) collecting
// .ips files. It tolerates unreadable entries and a missing directory,
// yielding whatever it could enumerate rather than an error.
func findIPSFiles(dir string) ([]string, error) {
	var out []string
	// The callback always returns nil so the walk continues past any
	// unreadable subtree; entries are only collected when walkErr is nil.
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr == nil && !d.IsDir() && filepath.Ext(p) == ".ips" {
			out = append(out, p)
		}
		return nil
	})
	return out, nil
}

func defaultDiagnosticReportsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "DiagnosticReports")
}

func runCrashList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", 25, "maximum reports to show")
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
	return runCrashListWithDeps(*asJSON, *limit, stdout, stderr, defaultCrashListDeps(*dir))
}

func runCrashListWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, deps crashListDeps) int {
	files, err := deps.list()
	if err != nil {
		fmt.Fprintf(stderr, "list reports: %v\n", err)
		return 1
	}
	swept, skipped := sweepCrashReports(files, deps.read)
	summaries := make([]crashSummary, 0, len(swept))
	for _, s := range swept {
		summaries = append(summaries, summarize(s))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].at.After(summaries[j].at) })
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}
	if asJSON {
		return encodeJSON(stdout, stderr, summaries)
	}
	renderCrashList(stdout, summaries, len(swept), skipped)
	return 0
}

func renderCrashList(w io.Writer, summaries []crashSummary, total, skipped int) {
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
		if !s.at.IsZero() {
			when = s.at.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-16s  %-28s %-8s %s\n", when, truncate(s.Process, 28), s.Kind, s.Exception)
		if detail := crashListDetail(s); detail != "" {
			fmt.Fprintf(w, "      %s\n", detail)
		}
	}
}

// crashListDetail is the second line under an entry: the incident id and the
// source file (which the user can pass to `spectra crash inspect`).
func crashListDetail(s crashSummary) string {
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
