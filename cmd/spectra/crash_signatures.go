package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/kaeawc/spectra/internal/crashquery"
)

func runCrashSignatures(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash signatures", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", crashquery.DefaultLimit, "maximum signatures to show")
	dir := fs.String("dir", defaultDiagnosticReportsDir(), "DiagnosticReports directory")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra crash signatures [--json] [--limit N] [--dir <path>]")
		fmt.Fprintln(stderr, "Group crash reports into recurring signatures, ranked by occurrence.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if !requireCrashDir("crash signatures", *dir, stderr) {
		return 1
	}
	return runCrashSignaturesWithDeps(*asJSON, *limit, stdout, stderr, crashquery.DirSource(*dir))
}

func runCrashSignaturesWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, src crashquery.Source) int {
	res, err := crashquery.Signatures(context.Background(), src, limit)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if asJSON {
		return encodeJSON(stdout, stderr, res.Signatures)
	}
	renderCrashSignatures(stdout, res.Signatures, res.Total)
	return 0
}

func renderCrashSignatures(w io.Writer, sigs []crashquery.Signature, totalReports int) {
	if len(sigs) == 0 {
		fmt.Fprintln(w, "No crash reports found.")
		return
	}
	fmt.Fprintf(w, "Crash signatures (%d distinct, %d report(s)):\n", len(sigs), totalReports)
	for _, s := range sigs {
		fmt.Fprintf(w, "  [%d×] %s  %s  %s\n", s.Count, s.Process, s.Exception, s.TopFrame)
		detail := ""
		if s.First != "" {
			detail = "first " + shortWhen(s.FirstAt, s.First) + " · last " + shortWhen(s.LastAt, s.Last)
		}
		if s.Sample != "" {
			if detail != "" {
				detail += " · "
			}
			detail += "e.g. " + s.Sample
		}
		if detail != "" {
			fmt.Fprintf(w, "        %s\n", detail)
		}
	}
}

func shortWhen(at time.Time, raw string) string {
	if !at.IsZero() {
		return at.Format("2006-01-02 15:04")
	}
	return raw
}
