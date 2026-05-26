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
	fdBreakdown := fs.Bool("fd-breakdown", false, "Show typed FD columns (implies --deep)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fdBreakdown {
		*deep = true
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

	printProcessRows(procs, *deep, *fdBreakdown)
	return 0
}

func printProcessRows(procs []process.Info, deep, fdBreakdown bool) {
	if fdBreakdown {
		fmt.Printf("%-7s  %-6s  %-8s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %s\n", "PID", "THR", "RSS", "FDS", "PTY", "SOCK", "REG", "PIPE", "CHR", "KQ", "OTH", "COMMAND")
		fmt.Println(strings.Repeat("-", 105))
	} else if deep {
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
		switch {
		case fdBreakdown:
			printProcessFDBreakdownRow(p, thr)
		case deep:
			printProcessDeepRow(p, thr, cpu)
		default:
			fmt.Printf("%-7d  %-6s  %-10s  %-10s  %s\n",
				p.PID, thr, humanSizeKiB(p.RSSKiB), cpu, truncate(p.Command, 45))
		}
	}
}

func printProcessFDBreakdownRow(p process.Info, thr string) {
	b := p.FDBreakdown
	if b == nil {
		b = &process.FDBreakdown{}
	}
	fds := processFDString(p)
	fmt.Printf("%-7d  %-6s  %-8s  %-5s  %-5d  %-5d  %-5d  %-5d  %-5d  %-5d  %-5d  %s\n",
		p.PID, thr, humanSizeKiB(p.RSSKiB), fds, b.PTY, b.Socket, b.Regular, b.Pipe, b.Char, b.Kqueue, b.Other, truncate(p.Command, 40))
}

func printProcessDeepRow(p process.Info, thr, cpu string) {
	fmt.Printf("%-7d  %-6s  %-10s  %-10s  %-5s  %s\n",
		p.PID, thr, humanSizeKiB(p.RSSKiB), cpu, processFDString(p), truncate(p.Command, 45))
}

func processFDString(p process.Info) string {
	if p.OpenFDs > 0 {
		return strconv.Itoa(p.OpenFDs)
	}
	return "-"
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
