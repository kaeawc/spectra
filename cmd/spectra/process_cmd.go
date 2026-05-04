package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/process"
)

func runProcess(args []string) int {
	fs := flag.NewFlagSet("spectra process", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	sortBy := fs.String("sort", "rss", "Sort by: rss, pid, or cmd")
	topN := fs.Int("top", 0, "Show only top N processes (0 = all)")
	deep := fs.Bool("deep", false, "Enrich with open FD count and listening ports via lsof (slower)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	procs := process.CollectAll(context.Background(), process.CollectOptions{Deep: *deep})
	if len(procs) == 0 {
		fmt.Fprintln(os.Stderr, "no processes found")
		return 0
	}

	switch *sortBy {
	case "pid":
		sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	case "cmd":
		sort.Slice(procs, func(i, j int) bool { return procs[i].Command < procs[j].Command })
	default: // rss
		sort.Slice(procs, func(i, j int) bool { return procs[i].RSSKiB > procs[j].RSSKiB })
	}

	if *topN > 0 && *topN < len(procs) {
		procs = procs[:*topN]
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(procs)
		return 0
	}

	if *deep {
		fmt.Printf("%-7s  %-6s  %-10s  %-10s  %-5s  %s\n", "PID", "THR", "RSS", "CPU%", "FDS", "COMMAND")
		fmt.Println(strings.Repeat("-", 82))
	} else {
		fmt.Printf("%-7s  %-6s  %-10s  %-10s  %s\n", "PID", "THR", "RSS", "CPU%", "COMMAND")
		fmt.Println(strings.Repeat("-", 76))
	}
	for _, p := range procs {
		thr := "-"
		if p.ThreadCount > 0 {
			thr = strconv.Itoa(p.ThreadCount)
		}
		cpu := "-"
		if p.CPUPct > 0 {
			cpu = fmt.Sprintf("%.1f%%", p.CPUPct)
		}
		if *deep {
			fds := "-"
			if p.OpenFDs > 0 {
				fds = strconv.Itoa(p.OpenFDs)
			}
			fmt.Printf("%-7d  %-6s  %-10s  %-10s  %-5s  %s\n",
				p.PID, thr, humanSizeKiB(p.RSSKiB), cpu, fds, truncate(p.Command, 45))
		} else {
			fmt.Printf("%-7d  %-6s  %-10s  %-10s  %s\n",
				p.PID, thr, humanSizeKiB(p.RSSKiB), cpu, truncate(p.Command, 45))
		}
	}
	return 0
}

func humanSizeKiB(kib int64) string {
	if kib == 0 {
		return "-"
	}
	const mib = 1024
	const gib = 1024 * mib
	switch {
	case kib >= gib:
		return fmt.Sprintf("%.1fG", float64(kib)/gib)
	case kib >= mib:
		return fmt.Sprintf("%.1fM", float64(kib)/mib)
	default:
		return fmt.Sprintf("%dK", kib)
	}
}
