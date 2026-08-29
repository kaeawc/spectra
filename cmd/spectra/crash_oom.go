package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/crashreport"
)

// jetsamBugTypes are numeric .ips bug_type codes that mark a jetsam /
// low-memory kill. Kept small and conservative.
var jetsamBugTypes = map[string]bool{"298": true}

type oomKill struct {
	Time     string `json:"time"`
	Process  string `json:"process"`
	Reason   string `json:"reason"`
	Limit    string `json:"limit,omitempty"`
	Observed string `json:"observed,omitempty"`
	File     string `json:"file"`
	at       time.Time
}

// classifyMemoryKill reports whether a report is a memory/OOM termination and,
// if so, the attributed kill. The reliable path is EXC_RESOURCE(MEMORY), which
// the crash decoder already resolves; jetsam bug_types are a best-effort path.
func classifyMemoryKill(r *crashreport.Report) (oomKill, bool) {
	if r.Resource != nil && r.Resource.Flavor == "MEMORY" {
		return oomKill{
			Process:  r.Process,
			Reason:   r.Resource.Explanation,
			Limit:    r.Resource.Limit,
			Observed: r.Resource.Observed,
		}, true
	}
	if jetsamBugTypes[r.BugType] {
		return oomKill{Process: r.Process, Reason: "jetsam / low-memory kill"}, true
	}
	return oomKill{}, false
}

func collectMemoryKills(swept []sweptReport) []oomKill {
	var out []oomKill
	for _, s := range swept {
		kill, ok := classifyMemoryKill(s.Report)
		if !ok {
			continue
		}
		kill.Time = s.Report.Time
		kill.File = s.File
		if t, err := time.Parse(ipsTimeLayout, s.Report.Time); err == nil {
			kill.at = t
		}
		out = append(out, kill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out
}

func runCrashOOM(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("crash oom", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	limit := fs.Int("limit", 25, "maximum kills to show")
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
	return runCrashOOMWithDeps(*asJSON, *limit, stdout, stderr, defaultCrashListDeps(*dir))
}

func runCrashOOMWithDeps(asJSON bool, limit int, stdout, stderr io.Writer, deps crashListDeps) int {
	files, err := deps.list()
	if err != nil {
		fmt.Fprintf(stderr, "list reports: %v\n", err)
		return 1
	}
	swept, _ := sweepCrashReports(files, deps.read)
	kills := collectMemoryKills(swept)
	if limit > 0 && len(kills) > limit {
		kills = kills[:limit]
	}
	if asJSON {
		return encodeJSON(stdout, stderr, kills)
	}
	renderOOM(stdout, kills, len(swept))
	return 0
}

func renderOOM(w io.Writer, kills []oomKill, totalReports int) {
	if len(kills) == 0 {
		fmt.Fprintf(w, "No memory/OOM kills among %d report(s).\n", totalReports)
		return
	}
	fmt.Fprintf(w, "Memory / OOM kills (%d of %d report(s)):\n", len(kills), totalReports)
	for _, k := range kills {
		when := k.Time
		if !k.at.IsZero() {
			when = k.at.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "  %-16s  %-24s %s%s\n", when, truncate(k.Process, 24), k.Reason, oomLimitNote(k))
		if k.File != "" {
			fmt.Fprintf(w, "        %s\n", k.File)
		}
	}
}

func oomLimitNote(k oomKill) string {
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
