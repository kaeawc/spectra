package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// emitProcessFamilies renders `spectra process --group-by family`.
func emitProcessFamilies(w io.Writer, procs []process.Info, now time.Time, topN int, asJSON bool) int {
	fams := process.GroupFamilies(procs, now, 0)
	if topN > 0 && topN < len(fams) {
		fams = fams[:topN]
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(fams); err != nil {
			return 1
		}
		return 0
	}
	printProcessFamilies(w, fams)
	return 0
}

// groupByConflict returns an error message when --group-by is combined with a
// flag that would change or break family attribution (filters drop ancestors;
// --sort and --tree do not apply to family rows), or is not a known grouping.
func groupByConflict(fs *flag.FlagSet, groupBy string) string {
	if groupBy != "family" {
		return fmt.Sprintf("invalid --group-by %q (supported: family)", groupBy)
	}
	var bad []string
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "sort", "tree", "ppid", "user", "min-rss", "min-vsz", "min-elapsed":
			bad = append(bad, "--"+f.Name)
		}
	})
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("--group-by family cannot be combined with %s", strings.Join(bad, ", "))
}

func printProcessFamilies(w io.Writer, fams []process.Family) {
	fmt.Fprintf(w, "%-48s  %-6s  %-9s  %-6s  %-9s  %s\n", "FAMILY", "PROCS", "CPU%", "SHARE", "AGE", "HEAVIEST")
	fmt.Fprintln(w, strings.Repeat("-", 110))
	for _, f := range fams {
		age := "-"
		if f.Age > 0 {
			age = formatDuration(f.Age)
		}
		fmt.Fprintf(w, "%-48s  %-6d  %-9s  %-6s  %-9s  %s\n", truncate(f.Name, 48), f.Members,
			fmt.Sprintf("%.1f%%", f.CPUPct), fmt.Sprintf("%.0f%%", f.SharePct), age, formatMembers(f.Heaviest))
	}
}
