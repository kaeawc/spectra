package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/procdiag"
)

// procdiagInspector samples a pid and returns its diagnosis.
type procdiagInspector func(ctx context.Context, pid int, opts procdiag.Options) (procdiag.Report, error)

func runProcdiag(args []string) int {
	return runProcdiagWithIO(args, os.Stdout, os.Stderr, procdiag.Inspect)
}

func runProcdiagWithIO(args []string, stdout, stderr io.Writer, inspect procdiagInspector) int {
	fs := flag.NewFlagSet("spectra procdiag", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	interval := fs.Duration("interval", time.Second, "Time between the two samples (longer separates leaks from noise)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if hostos.Resolve(hostos.Unknown) != hostos.Linux {
		fmt.Fprintf(stderr, "spectra procdiag: %v (use `spectra sample` or `spectra hang` on macOS)\n", hostos.Unsupported("/proc process diagnosis"))
		return 1
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra procdiag [--interval <d>] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	if *interval <= 0 {
		fmt.Fprintln(stderr, "--interval must be positive")
		return 2
	}
	rep, err := inspect(context.Background(), pid, procdiag.Options{Interval: *interval})
	if err != nil {
		if errors.Is(err, procdiag.ErrNoProcess) {
			fmt.Fprintf(stderr, "spectra procdiag: no such process %d\n", pid)
			return 1
		}
		fmt.Fprintf(stderr, "spectra procdiag: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printProcdiag(stdout, rep)
	}
	return exitForFindings(rep)
}

// exitForFindings returns 3 when anything at warning or above was found, so a
// script can gate on it; healthy and info-only reports exit 0.
func exitForFindings(rep procdiag.Report) int {
	for _, f := range rep.Findings {
		if f.Severity != procdiag.SeverityInfo {
			return 3
		}
	}
	return 0
}

func printProcdiag(w io.Writer, r procdiag.Report) {
	fmt.Fprintf(w, "=== pid %d (%s) — %s ===\n", r.PID, r.Comm, r.Verdict)
	if r.Cmdline != "" {
		fmt.Fprintf(w, "  cmd:      %s\n", truncate(r.Cmdline, 100))
	}
	fmt.Fprintf(w, "  sampled:  %.1fs, %d thread(s) %v\n", r.IntervalSec, r.ThreadCount, r.StateCounts)
	fmt.Fprintf(w, "  cpu:      %.0f%% of one core\n", r.CPUPct)
	fmt.Fprintf(w, "  memory:   rss %d MiB (%+.1f MiB/s), anon %d MiB, swap %d MiB\n",
		r.Memory.RSSKiB/1024, r.Memory.RSSRateKiBPS/1024, r.Memory.AnonKiB/1024, r.Memory.SwapKiB/1024)
	limit := "unlimited"
	if r.FDs.Limit > 0 {
		limit = strconv.FormatInt(r.FDs.Limit, 10)
	}
	fmt.Fprintf(w, "  fds:      %d / %s (%+.1f/s)\n", r.FDs.Count, limit, r.FDs.RatePerSec)
	if r.IO != nil {
		fmt.Fprintf(w, "  disk io:  read %d KiB, wrote %d KiB\n", r.IO.ReadBytes/1024, r.IO.WriteBytes/1024)
	}
	if r.Cgroup != nil {
		fmt.Fprintf(w, "  cgroup:   %s\n", r.Cgroup.Path)
	}
	fmt.Fprintln(w, "  top threads:")
	for i, t := range r.Threads {
		if i == 5 {
			break
		}
		where := t.Syscall
		if t.WChan != "" {
			where += " @" + t.WChan
		}
		fmt.Fprintf(w, "    %7d %-15s %s %5.1f%%  %s\n", t.TID, truncate(t.Comm, 15), t.State, t.CPUPct, where)
	}
	if len(r.Findings) == 0 {
		fmt.Fprintln(w, "  no problems found in this window")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(w, "  [%s] %s: %s\n", f.Severity, f.Kind, f.Summary)
		for _, e := range f.Evidence {
			fmt.Fprintf(w, "         · %s\n", e)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(w, "  note: %s\n", n)
	}
}
