package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/kaeawc/spectra/internal/crashquery"
)

func runCrashOOM(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash oom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", crashquery.DefaultLimit, "maximum kills to show")
	dir := fs.String("dir", defaultDiagnosticReportsDir(), "DiagnosticReports directory")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash oom [--json] [--limit N] [--dir <path>]")
		fmt.Fprintln(stderr, "List memory / OOM terminations (EXC_RESOURCE memory limits and jetsam kills).")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if !requireCrashDir("crash oom", *dir, stderr) {
		return 1
	}
	return runCrashOOMWithDeps(*asJSON, *limit, stdout, stderr, crashquery.DirSource(*dir))
}

func runCrashOOMWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, src crashquery.Source) int {
	res, err := crashquery.OOM(context.Background(), src, limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return encodeJSON(stdout, stderr, res.Kills)
	}
	renderOOM(stdout, res.Kills, res.Total)
	return 0
}

func renderOOM(w io.Writer, kills []crashquery.OOMKill, totalReports int) {
	if len(kills) == 0 {
		fmt.Fprintf(w, "No memory/OOM kills among %d report(s).\n", totalReports)
		return
	}
	fmt.Fprintf(w, "Memory / OOM kills (%d of %d report(s)):\n", len(kills), totalReports)
	for _, k := range kills {
		when := k.Time
		if !k.At.IsZero() {
			when = k.At.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-16s  %-24s %s%s\n", when, truncate(k.Process, 24), k.Reason, oomLimitNote(k))
		if k.File != "" {
			fmt.Fprintf(w, "        %s\n", k.File)
		}
	}
}

func oomLimitNote(k crashquery.OOMKill) string {
	if k.Limit == "" && k.Observed == "" {
		return ""
	}
	return fmt.Sprintf(" (limit %s, observed %s)", orDashKill(k.Limit), orDashKill(k.Observed))
}

func orDashKill(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
