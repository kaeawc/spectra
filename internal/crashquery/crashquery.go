// Package crashquery builds the values `spectra crash list|signatures|oom|inspect
// --json` print, from a directory of .ips reports or one report, so the CLI
// and the daemon return the same JSON. It reads only.
package crashquery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/hostos"
)

// IPSTimeLayout is the timestamp format in an .ips header, e.g.
// "2026-08-05 23:17:34.00 -0500".
const IPSTimeLayout = "2006-01-02 15:04:05.00 -0700"

// DefaultLimit is the CLI's --limit default for list, signatures and oom.
const DefaultLimit = 25

// ReportsFeature names macOS's DiagnosticReports directory in the
// unsupported-platform error given when no directory is passed off macOS.
const ReportsFeature = "macOS DiagnosticReports"

// Source enumerates and reads .ips reports.
type Source struct {
	List func(context.Context) ([]string, error)
	Read func(string) ([]byte, error)
}

// DirSource reads the .ips files under dir.
func DirSource(dir string) Source {
	return Source{
		List: func(ctx context.Context) ([]string, error) { return FindIPSFiles(ctx, dir) },
		Read: os.ReadFile,
	}
}

// DefaultDir is where macOS collects .ips reports for the user whose home is
// home, or "" when there is none: off macOS, or with no home directory.
func DefaultDir(k hostos.Kind, home string) string {
	if hostos.Resolve(k) != hostos.Darwin || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "DiagnosticReports")
}

// FindIPSFiles walks dir (including Retired/ and dotfile reports) collecting
// .ips files. It tolerates unreadable entries and a missing directory,
// yielding whatever it could enumerate; only ctx ending is an error.
func FindIPSFiles(ctx context.Context, dir string) ([]string, error) {
	var out []string
	// The callback returns nil past any unreadable subtree; entries are only
	// collected when walkErr is nil.
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if walkErr == nil && !d.IsDir() && filepath.Ext(p) == ".ips" {
			out = append(out, p)
		}
		return nil
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Swept pairs a parsed crash report with its source file.
type Swept struct {
	File   string
	Report *crashreport.Report
}

// Sweep parses each file, skipping legacy, unreadable and unparseable ones
// (returned as the skipped count) so one bad report never breaks a sweep.
func Sweep(ctx context.Context, files []string, read func(string) ([]byte, error)) ([]Swept, int, error) {
	var out []Swept
	skipped := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
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
		out = append(out, Swept{File: f, Report: rep})
	}
	return out, skipped, nil
}

func sweep(ctx context.Context, src Source) ([]Swept, int, error) {
	files, err := src.List(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list reports: %w", err)
	}
	return Sweep(ctx, files, src.Read)
}

// ParseTime parses an .ips timestamp, returning the zero time when it does
// not parse.
func ParseTime(s string) time.Time {
	t, err := time.Parse(IPSTimeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Summary is one row of `spectra crash list --json`.
type Summary struct {
	Time      string `json:"time"`
	Process   string `json:"process"`
	Kind      string `json:"kind"`
	Exception string `json:"exception,omitempty"`
	Incident  string `json:"incident_id,omitempty"`
	File      string `json:"file"`
	// At is Time parsed, zero when it does not parse.
	At time.Time `json:"-"`
}

// Summarize is the list row for s.
func Summarize(s Swept) Summary {
	r := s.Report
	return Summary{
		Time: r.Time, Process: r.Process, Kind: r.Kind,
		Exception: r.Exception, Incident: r.IncidentID, File: s.File,
		At: ParseTime(r.Time),
	}
}

// ListResult is a crash list. Summaries is the --json value.
type ListResult struct {
	Summaries []Summary
	// Total counts the parsed reports, Skipped the ones that did not parse.
	Total, Skipped int
}

// List summarizes the reports in src, newest first, keeping at most limit
// (all when limit <= 0).
func List(ctx context.Context, src Source, limit int) (ListResult, error) {
	swept, skipped, err := sweep(ctx, src)
	if err != nil {
		return ListResult{}, err
	}
	summaries := make([]Summary, 0, len(swept))
	for _, s := range swept {
		summaries = append(summaries, Summarize(s))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].At.After(summaries[j].At) })
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return ListResult{Summaries: summaries, Total: len(swept), Skipped: skipped}, nil
}

// Load reads and parses one .ips report. The error text is what the CLI
// prints.
func Load(path string, read func(string) ([]byte, error)) (*crashreport.Report, error) {
	data, err := read(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	report, err := crashreport.Parse(data)
	if err != nil {
		if errors.Is(err, crashreport.ErrLegacyFormat) {
			return nil, legacyError{path}
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return report, nil
}

type legacyError struct{ path string }

func (e legacyError) Error() string {
	return e.path + " is a legacy plain-text crash report; spectra decodes modern .ips JSON reports."
}

func (legacyError) Unwrap() error { return crashreport.ErrLegacyFormat }

// InspectOptions configure Inspect.
type InspectOptions struct {
	Read func(string) ([]byte, error)
	Open crashreport.OpenFunc
	// SourceLines resolves frames to file:line with Source.
	SourceLines bool
	Source      crashreport.SourceLineOptions
}

// Inspect is the `spectra crash inspect --json` value for the report at path:
// the decoded report, its frames resolved to source lines when asked, and
// each image checked against the binary on disk.
func Inspect(ctx context.Context, path string, opts InspectOptions) (*crashreport.Report, error) {
	report, err := Load(path, opts.Read)
	if err != nil {
		return nil, err
	}
	if opts.SourceLines {
		report.ResolveSourceLines(ctx, opts.Source)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report.CheckImagesOnDisk(opts.Open)
	return report, nil
}
