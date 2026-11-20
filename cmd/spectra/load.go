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

	"github.com/kaeawc/spectra/internal/hostload"
)

// loadDeps are the seams runLoadWithIO uses so tests need no real host.
type loadDeps = hostload.ReportDeps

type loadReport = hostload.Report

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func runLoad(args []string) int {
	return runLoadWithIO(args, os.Stdout, os.Stderr, defaultLoadDeps())
}

func defaultLoadDeps() loadDeps { return hostload.DefaultReportDeps() }

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
	def := hostload.DefaultReportOptions()
	top := fs.Int("top", def.Top, "Rows per top table")
	var roots stringList
	fs.Var(&roots, "root", "Directory holding worktrees; cause is \"roots\" when load leads there (repeatable)")
	maxLoad := fs.Float64("max-load-per-cpu", def.MaxLoadPerCPU, "Hold when 1-minute load is at or above this times the CPU count")
	minFree := fs.Float64("min-free-gb", def.MinFreeGB, "Hold when available memory is below this many GiB (also held when memory cannot be read)")
	sample := fs.Duration("sample", def.Sample, "Linux: measure CPU over this window between two /proc readings; 0 uses lifetime averages")
	backoff := fs.Float64("backoff-free-gb", def.BackoffFreeGB, "Back off when available memory is below this many GiB")
	bootWindow := fs.Duration("boot-window", def.BootWindow, "Count a simulator or emulator as booted recently when it started within this window")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := hostload.ReportOptions{
		Top: *top, Roots: roots, MaxLoadPerCPU: *maxLoad, MinFreeGB: *minFree, BackoffFreeGB: *backoff,
		Sample: *sample, BootWindow: *bootWindow,
	}
	if fs.NArg() > 0 || opts.Validate() != nil {
		fmt.Fprintln(stderr, "spectra load: invalid arguments (see --help)")
		return 2
	}

	rep, err := hostload.BuildReport(context.Background(), opts, deps)
	if err != nil {
		fmt.Fprintf(stderr, "spectra load: %v\n", err)
		return 1
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
	switch hostload.CPUBasis(r.CPUBasis) {
	case hostload.BasisWindow:
		fmt.Fprintf(w, "cpu      measured over a %d ms window\n", r.CPUWindowMS)
	case hostload.BasisRecent:
		fmt.Fprintln(w, "cpu      recent average (ps decaying average)")
	default:
		fmt.Fprintln(w, "cpu      lifetime average (no recent window)")
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
	writeDevices(w, r.Devices)
	writeSpotlight(w, r.Spotlight)
	fmt.Fprintf(w, "elapsed  %d ms\n", r.ElapsedMS)
}

func writeDevices(w io.Writer, d hostload.Devices) {
	fmt.Fprintf(w, "devices  %d booted (%d simulators, %d emulators), %d in the last %s\n",
		d.Booted, d.Simulators, d.Emulators, d.BootedRecently, time.Duration(d.BootWindowSeconds)*time.Second)
	for _, dev := range d.List {
		line := fmt.Sprintf("device   %-9s %s  pid %d  %3d procs  %5.1f%% cpu  age %s", dev.Kind, dev.ID, dev.PID, dev.Members, dev.CPUPct, time.Duration(dev.AgeSeconds)*time.Second)
		if dev.Recent {
			line += "  recent"
		}
		if dev.Root != "" {
			line += "  root " + dev.Root
		}
		fmt.Fprintln(w, line)
	}
	for _, rd := range d.ByRoot {
		fmt.Fprintf(w, "devices  %s: %d booted, %d recently\n", rd.Root, rd.Booted, rd.BootedRecently)
	}
}

func writeSpotlight(w io.Writer, roots []hostload.RootSpotlight) {
	for _, rs := range roots {
		if rs.Unsupported != "" {
			fmt.Fprintf(w, "spotlight %s  %s\n", rs.Root, rs.Unsupported)
			continue
		}
		excl := "unknown"
		if rs.Excluded != nil {
			excl = "no"
			if *rs.Excluded {
				excl = "yes"
			}
		}
		vol := "volume unknown"
		if rs.Volume != nil {
			vol = "volume " + rs.Volume.MountPoint + " " + rs.Volume.Status.String()
		}
		fmt.Fprintf(w, "spotlight %s  excluded %s  %s\n", rs.Root, excl, vol)
		fmt.Fprintf(w, "hint     %s\n", rs.Hint)
	}
}

func writeRows(w io.Writer, label string, rows []hostload.ProcRow) {
	for _, p := range rows {
		fmt.Fprintf(w, "%-8s %6d  %5.1f%% cpu  %7.0f MB  %s  %s\n", label, p.PID, p.CPUPct, float64(p.RSSKiB)/1024, p.Command, p.Dir)
	}
}
