package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

// loadDeps are the seams runLoadWithIO uses so tests need no real host.
type loadDeps struct {
	Host  hostload.Host
	Procs func(ctx context.Context) []process.Info
	FS    hostload.FS
	Now   func() time.Time
	// EvalSymlinks resolves --root paths; nil means no resolution.
	EvalSymlinks func(string) (string, error)
}

func resolveRoots(roots []string, eval func(string) (string, error)) []hostload.Root {
	out := make([]hostload.Root, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		root := hostload.Root{Path: r, Resolved: filepath.Clean(r)}
		if eval != nil {
			if res, err := eval(r); err == nil {
				root.Resolved = res
			}
		}
		out = append(out, root)
	}
	return out
}

type loadReport struct {
	Verdict    string               `json:"verdict"`
	Reason     string               `json:"reason"`
	ExitCode   int                  `json:"exit_code"`
	Load1      float64              `json:"load1"`
	LoadLimit  float64              `json:"load_limit"`
	CPUs       int                  `json:"cpus"`
	AvailGB    *float64             `json:"available_gb"`    // GiB; null when unreadable
	MinFreeGB  float64              `json:"min_free_gb"`     // GiB
	BackoffGB  float64              `json:"backoff_free_gb"` // GiB
	Cause      hostload.Cause       `json:"cause"`
	TopCPU     []hostload.ProcRow   `json:"top_cpu"`
	TopRSS     []hostload.ProcRow   `json:"top_rss"`
	Families   []hostload.FamilyRow `json:"families"`
	Dirs       []hostload.DirUsage  `json:"dirs"`
	Unknown    hostload.DirUsage    `json:"unknown"`
	Notes      []string             `json:"notes,omitempty"`
	ElapsedMS  int64                `json:"elapsed_ms"`
	ProcessCnt int                  `json:"process_count"`
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runLoad(args []string) int {
	return runLoadWithIO(args, os.Stdout, os.Stderr, defaultLoadDeps())
}

func defaultLoadDeps() loadDeps {
	host := hostload.Host{OS: hostos.Resolve(hostos.Unknown), Runner: proc.OS{}}
	return loadDeps{
		Host: host,
		Procs: func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{
				Runner: host.Runner,
				OS:     host.OS,
				// A non-nil collector that returns nothing skips per-process
				// libproc work on macOS; ps already supplies what we need.
				DetailCollector: func([]process.Info) map[int]process.Details { return map[int]process.Details{} },
			})
		},
		FS:  hostload.FS{Exists: func(p string) bool { _, err := os.Lstat(p); return err == nil }},
		Now: time.Now,

		EvalSymlinks: filepath.EvalSymlinks,
	}
}

func runLoadWithIO(args []string, stdout, stderr io.Writer, deps loadDeps) int {
	fs := flag.NewFlagSet("spectra load", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage: spectra load [flags]

Can this machine take another job, and if not, who is using it?

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(stderr, `
Exit codes:
  0  admit    load and memory are within limits
  3  hold     1-minute load is at or above its limit, available memory is below
             --min-free-gb, or available memory could not be read
  4  backoff  available memory is below --backoff-free-gb
  1  error
  2  usage
`)
	}
	asJSON := fs.Bool("json", false, "Emit JSON instead of text")
	top := fs.Int("top", 5, "Rows per top table")
	var roots stringList
	fs.Var(&roots, "root", "Directory holding worktrees; cause is \"roots\" when load leads there (repeatable)")
	maxLoad := fs.Float64("max-load-per-cpu", 1.5, "Hold when 1-minute load is at or above this times the CPU count")
	minFree := fs.Float64("min-free-gb", 8, "Hold when available memory is below this many GiB (also held when memory cannot be read)")
	backoff := fs.Float64("backoff-free-gb", 4, "Back off when available memory is below this many GiB")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *top < 1 || *maxLoad <= 0 || *minFree < 0 || *backoff < 0 {
		fmt.Fprintln(stderr, "spectra load: invalid arguments (see --help)")
		return 2
	}

	ctx := context.Background()
	start := deps.Now()
	load1, err := deps.Host.Load1(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "spectra load: %v\n", err)
		return 1
	}
	reading := hostload.Reading{Load1: load1, CPUs: deps.Host.CPUs()}
	var notes []string
	if avail, err := deps.Host.Available(ctx); err != nil {
		notes = append(notes, fmt.Sprintf("available memory unknown: %v", err))
	} else {
		reading.AvailBytes, reading.MemKnown = avail, true
	}
	dec := hostload.Decide(reading, hostload.Thresholds{MaxLoadPerCPU: *maxLoad, MinFreeGB: *minFree, BackoffFreeGB: *backoff})

	procs := deps.Procs(ctx)
	pids := make([]int, len(procs))
	for i, p := range procs {
		pids[i] = p.PID
	}
	an := hostload.Analyze(procs, deps.Host.Cwds(ctx, pids), hostload.Options{TopN: *top, Roots: resolveRoots(roots, deps.EvalSymlinks), FS: deps.FS, Now: deps.Now()})

	rep := loadReport{
		Verdict: string(dec.Verdict), Reason: dec.Reason, ExitCode: dec.Verdict.ExitCode(),
		Load1: reading.Load1, LoadLimit: dec.LoadLimit, CPUs: reading.CPUs,
		MinFreeGB: *minFree, BackoffGB: *backoff,
		Cause: an.Cause, TopCPU: an.TopCPU, TopRSS: an.TopRSS, Families: an.Families,
		Dirs: an.Dirs, Unknown: an.Unknown, Notes: notes,
		ElapsedMS: deps.Now().Sub(start).Milliseconds(), ProcessCnt: len(procs),
	}
	if reading.MemKnown {
		gb := float64(reading.AvailBytes) / (1 << 30)
		rep.AvailGB = &gb
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "spectra load: %v\n", err)
			return 1
		}
		return rep.ExitCode
	}
	writeLoadText(stdout, rep)
	return rep.ExitCode
}

func writeLoadText(w io.Writer, r loadReport) {
	fmt.Fprintf(w, "verdict  %s  %s\n", strings.ToUpper(r.Verdict), r.Reason)
	fmt.Fprintf(w, "load     %.2f  %d CPUs, limit %.2f\n", r.Load1, r.CPUs, r.LoadLimit)
	if r.AvailGB != nil {
		fmt.Fprintf(w, "memory   %.1f GiB available  min %.1f, backoff %.1f\n", *r.AvailGB, r.MinFreeGB, r.BackoffGB)
	} else {
		fmt.Fprintf(w, "memory   unknown  min %.1f, backoff %.1f\n", r.MinFreeGB, r.BackoffGB)
	}
	fmt.Fprintf(w, "cause    %s  %s\n", r.Cause.Kind, r.Cause.Detail)
	for _, n := range r.Notes {
		fmt.Fprintf(w, "note     %s\n", n)
	}
	writeRows(w, "top-cpu", r.TopCPU)
	writeRows(w, "top-rss", r.TopRSS)
	for _, f := range r.Families {
		fmt.Fprintf(w, "family   %5.1f%% cpu  %7.0f MB  %3d procs  %s (%s)\n", f.CPUPct, float64(f.RSSKiB)/1024, f.Members, f.Name, f.Kind)
	}
	for _, d := range r.Dirs {
		fmt.Fprintf(w, "dir      %5.1f%% cpu  %7.0f MB  %3d procs  %s\n", d.CPUPct, float64(d.RSSKiB)/1024, d.Procs, d.Dir)
	}
	if r.Unknown.Procs > 0 {
		fmt.Fprintf(w, "dir      %5.1f%% cpu  %7.0f MB  %3d procs  %s (cwd unreadable)\n", r.Unknown.CPUPct, float64(r.Unknown.RSSKiB)/1024, r.Unknown.Procs, r.Unknown.Dir)
	}
	fmt.Fprintf(w, "elapsed  %d ms\n", r.ElapsedMS)
}

func writeRows(w io.Writer, label string, rows []hostload.ProcRow) {
	for _, p := range rows {
		fmt.Fprintf(w, "%-8s %6d  %5.1f%% cpu  %7.0f MB  %s  %s\n", label, p.PID, p.CPUPct, float64(p.RSSKiB)/1024, p.Command, p.Dir)
	}
}
