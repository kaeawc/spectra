package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
)

// crashSignature is a group of reports that share the same process, exception,
// and top faulting frame.
type crashSignature struct {
	Process   string `json:"process"`
	Exception string `json:"exception,omitempty"`
	TopFrame  string `json:"top_frame"`
	Count     int    `json:"count"`
	First     string `json:"first_seen,omitempty"`
	Last      string `json:"last_seen,omitempty"`
	Sample    string `json:"sample_file,omitempty"`
	Incident  string `json:"sample_incident,omitempty"`
	firstAt   time.Time
	lastAt    time.Time
}

// topFaultingFrame returns the first frame of the report's faulting thread, or
// "(no frame)" when there is none.
func topFaultingFrame(r *crashreport.Report) string {
	for _, t := range r.Threads {
		if t.Triggered && len(t.Frames) > 0 {
			return t.Frames[0]
		}
	}
	if r.FaultingThread >= 0 && r.FaultingThread < len(r.Threads) {
		if fr := r.Threads[r.FaultingThread].Frames; len(fr) > 0 {
			return fr[0]
		}
	}
	return "(no frame)"
}

// aggregateSignatures groups reports by process+exception+top-frame and ranks
// them by occurrence count (most-recent breaks ties).
func aggregateSignatures(swept []sweptReport) []crashSignature {
	byKey := map[string]*crashSignature{}
	for _, s := range swept {
		frame := topFaultingFrame(s.Report)
		key := s.Report.Process + "\x00" + s.Report.Exception + "\x00" + frame
		sig, ok := byKey[key]
		if !ok {
			sig = &crashSignature{Process: s.Report.Process, Exception: s.Report.Exception, TopFrame: frame}
			byKey[key] = sig
		}
		observeSignature(sig, s)
	}
	out := make([]crashSignature, 0, len(byKey))
	for _, s := range byKey {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if !out[i].lastAt.Equal(out[j].lastAt) {
			return out[i].lastAt.After(out[j].lastAt)
		}
		return out[i].Process < out[j].Process
	})
	return out
}

func observeSignature(sig *crashSignature, s sweptReport) {
	sig.Count++
	at, err := time.Parse(ipsTimeLayout, s.Report.Time)
	if err != nil {
		if sig.Sample == "" {
			sig.Sample, sig.Incident = s.File, s.Report.IncidentID
		}
		return
	}
	if sig.firstAt.IsZero() || at.Before(sig.firstAt) {
		sig.firstAt, sig.First = at, s.Report.Time
	}
	if sig.lastAt.IsZero() || at.After(sig.lastAt) {
		sig.lastAt, sig.Last = at, s.Report.Time
		sig.Sample, sig.Incident = s.File, s.Report.IncidentID
	}
}

func runCrashSignatures(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash signatures", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", 25, "maximum signatures to show")
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
	return runCrashSignaturesWithDeps(*asJSON, *limit, stdout, stderr, defaultCrashListDeps(*dir))
}

func runCrashSignaturesWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, deps crashListDeps) int {
	files, err := deps.list()
	if err != nil {
		fmt.Fprintf(stderr, "list reports: %v\n", err)
		return 1
	}
	swept, _ := sweepCrashReports(files, deps.read)
	sigs := aggregateSignatures(swept)
	total := len(swept)
	if limit > 0 && len(sigs) > limit {
		sigs = sigs[:limit]
	}
	if asJSON {
		return encodeJSON(stdout, stderr, sigs)
	}
	renderCrashSignatures(stdout, sigs, total)
	return 0
}

func renderCrashSignatures(w io.Writer, sigs []crashSignature, totalReports int) {
	if len(sigs) == 0 {
		fmt.Fprintln(w, "No crash reports found.")
		return
	}
	fmt.Fprintf(w, "Crash signatures (%d distinct, %d report(s)):\n", len(sigs), totalReports)
	for _, s := range sigs {
		fmt.Fprintf(w, "  [%d×] %s  %s  %s\n", s.Count, s.Process, s.Exception, s.TopFrame)
		detail := ""
		if s.First != "" {
			detail = "first " + shortWhen(s.firstAt, s.First) + " · last " + shortWhen(s.lastAt, s.Last)
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
