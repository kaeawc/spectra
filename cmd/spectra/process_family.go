package main

import (
	"encoding/json"
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
