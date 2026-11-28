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
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

// collectProcesses and processNow are the process table and the clock
// runProcess reads; contract tests replace them.
var (
	collectProcesses = process.CollectAll
	processNow       = time.Now
)

func runProcess(args []string) int {
	fs := flag.NewFlagSet("spectra process", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	sortBy := fs.String("sort", "rss", "Sort by: rss, vsz, cpu, mem, elapsed, startedat, pid, or cmd")
	topN := fs.Int("top", 0, "Show only top N processes (0 = all)")
	deep := fs.Bool("deep", false, "Enrich with open FD count and listening ports via lsof (slower)")
	fdBreakdown := fs.Bool("fd-breakdown", false, "Show typed FD columns (implies --deep)")
	tree := fs.Bool("tree", false, "Render a parent/child process tree")
	minRSS := fs.String("min-rss", "", "Minimum RSS (for example 500M, 2G)")
	minVSZ := fs.String("min-vsz", "", "Minimum VSZ (for example 1G)")
	minElapsed := fs.Duration("min-elapsed", 0, "Minimum elapsed runtime")
	ppid := fs.Int("ppid", -1, "Only direct children of PID")
	user := fs.String("user", "", "Only processes owned by user name")
	groupBy := fs.String("group-by", "", "Group processes: family (sums CPU per simulator device, Xcode, build tools, browsers)")
	classifyFlag := fs.Bool("classify", false, "Add role, runtime, owning app and (macOS) code signing per process")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fdBreakdown {
		*deep = true
	}
	if *groupBy != "" {
		if msg := groupByConflict(fs, *groupBy); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return 2
		}
	}
	filters, ok := processFiltersFromFlags(*minRSS, *minVSZ, *minElapsed, *ppid, *user)
	if !ok {
		return 2
	}

	procs := collectProcesses(context.Background(), process.CollectOptions{Deep: *deep})
	procs = filterProcesses(procs, filters)
	if len(procs) == 0 {
		fmt.Fprintln(os.Stderr, "no processes found")
		return 0
	}

	if *groupBy == "family" {
		return emitProcessFamilies(os.Stdout, procs, processNow(), *topN, *asJSON)
	}

	if !sortProcesses(procs, *sortBy) {
		fmt.Fprintf(os.Stderr, "invalid --sort %q\n", *sortBy)
		return 2
	}

	if *topN > 0 && *topN < len(procs) {
		procs = procs[:*topN]
	}
	if *classifyFlag {
		classifyProcesses(procs)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if *tree {
			_ = enc.Encode(process.BuildTree(procs))
			return 0
		}
		_ = enc.Encode(procs)
		return 0
	}

	cols := newProcessClassColumns(*classifyFlag)
	if *tree {
		printProcessTree(process.BuildTree(procs), cols)
		return 0
	}
	printProcessRows(procs, *deep, *fdBreakdown, cols)
	return 0
}

type processFilters struct {
	minRSSBytes uint64
	minVSZBytes uint64
	minElapsed  time.Duration
	ppid        int
	user        string
}

func processFiltersFromFlags(minRSS, minVSZ string, minElapsed time.Duration, ppid int, user string) (processFilters, bool) {
	rss, err := parseProcessSize(minRSS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --min-rss: %v\n", err)
		return processFilters{}, false
	}
	vsz, err := parseProcessSize(minVSZ)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --min-vsz: %v\n", err)
		return processFilters{}, false
	}
	return processFilters{
		minRSSBytes: rss,
		minVSZBytes: vsz,
		minElapsed:  minElapsed,
		ppid:        ppid,
		user:        user,
	}, true
}

func filterProcesses(procs []process.Info, filters processFilters) []process.Info {
	if filters == (processFilters{ppid: -1}) {
		return procs
	}
	out := make([]process.Info, 0, len(procs))
	for _, p := range procs {
		if processMatchesFilters(p, filters) {
			out = append(out, p)
		}
	}
	return out
}

func processMatchesFilters(p process.Info, filters processFilters) bool {
	if filters.minRSSBytes > 0 && processRSSBytes(p) < filters.minRSSBytes {
		return false
	}
	if filters.minVSZBytes > 0 && processVSZBytes(p) < filters.minVSZBytes {
		return false
	}
	if filters.minElapsed > 0 && p.Elapsed < filters.minElapsed {
		return false
	}
	if filters.ppid >= 0 && p.PPID != filters.ppid {
		return false
	}
	return filters.user == "" || p.User == filters.user
}

func sortProcesses(procs []process.Info, sortBy string) bool {
	switch sortBy {
	case "pid":
		sort.Slice(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
	case "cmd":
		sort.Slice(procs, func(i, j int) bool { return procs[i].Command < procs[j].Command })
	case "rss":
		sort.Slice(procs, func(i, j int) bool { return processRSSBytes(procs[i]) > processRSSBytes(procs[j]) })
	case "vsz":
		sort.Slice(procs, func(i, j int) bool { return processVSZBytes(procs[i]) > processVSZBytes(procs[j]) })
	case "cpu":
		sort.Slice(procs, func(i, j int) bool { return procs[i].CPUPercent > procs[j].CPUPercent })
	case "mem":
		sort.Slice(procs, func(i, j int) bool { return procs[i].MemPercent > procs[j].MemPercent })
	case "elapsed":
		sort.Slice(procs, func(i, j int) bool { return procs[i].Elapsed > procs[j].Elapsed })
	case "startedat":
		sort.Slice(procs, func(i, j int) bool { return procs[i].StartedAt.Before(procs[j].StartedAt) })
	default:
		return false
	}
	return true
}

func parseProcessSize(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	unit := raw[len(raw)-1]
	multiplier := uint64(1)
	number := raw
	switch unit {
	case 'k', 'K':
		multiplier = 1024
		number = raw[:len(raw)-1]
	case 'm', 'M':
		multiplier = 1024 * 1024
		number = raw[:len(raw)-1]
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		number = raw[:len(raw)-1]
	case 't', 'T':
		multiplier = 1024 * 1024 * 1024 * 1024
		number = raw[:len(raw)-1]
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("expected non-negative size")
	}
	return uint64(value * float64(multiplier)), nil
}

func processRSSBytes(p process.Info) uint64 {
	if p.RSSKiB <= 0 {
		return 0
	}
	return uint64(p.RSSKiB) * 1024
}

func processVSZBytes(p process.Info) uint64 {
	if p.VSZBytes > 0 {
		return p.VSZBytes
	}
	if p.VSizeKiB <= 0 {
		return 0
	}
	return uint64(p.VSizeKiB) * 1024
}

func printProcessRows(procs []process.Info, deep, fdBreakdown bool, cols processClassColumns) {
	if fdBreakdown {
		fmt.Printf("%-7s  %-6s  %-8s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %-5s  %s%s\n", "PID", "THR", "RSS", "FDS", "PTY", "SOCK", "REG", "PIPE", "CHR", "KQ", "OTH", cols.header(), "COMMAND")
		fmt.Println(strings.Repeat("-", 105+cols.width()))
	} else if deep {
		fmt.Printf("%-7s  %-6s  %-10s  %-10s  %-5s  %s%s\n", "PID", "THR", "RSS", "CPU%", "FDS", cols.header(), "COMMAND")
		fmt.Println(strings.Repeat("-", 82+cols.width()))
	} else {
		fmt.Printf("%-7s  %-7s  %-10s  %-10s  %-7s  %-7s  %-20s  %s%s\n", "PID", "PPID", "RSS", "VSZ", "CPU%", "MEM%", "STARTED", cols.header(), "COMMAND")
		fmt.Println(strings.Repeat("-", 112+cols.width()))
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
			printProcessFDBreakdownRow(p, thr, cols)
		case deep:
			printProcessDeepRow(p, thr, cpu, cols)
		default:
			fmt.Printf("%-7d  %-7d  %-10s  %-10s  %-7s  %-7s  %-20s  %s%s\n",
				p.PID, p.PPID, humanSizeKiB(p.RSSKiB), humanSizeBytes(processVSZBytes(p)), cpu,
				percentString(p.MemPercent), startedString(p.StartedAt), cols.row(p), truncate(p.Command, 40))
		}
	}
}

func printProcessTree(roots []*process.TreeNode, cols processClassColumns) {
	fmt.Printf("%-7s  %-7s  %-20s  %s%s\n", "PID", "PPID", "STARTED", cols.header(), "COMMAND")
	fmt.Println(strings.Repeat("-", 76+cols.width()))
	for _, root := range roots {
		printProcessTreeNode(root, 0, cols)
	}
}

func printProcessTreeNode(node *process.TreeNode, depth int, cols processClassColumns) {
	p := node.Info
	prefix := strings.Repeat("  ", depth)
	width := 50 - depth*2
	if width < 20 {
		width = 20
	}
	fmt.Printf("%-7d  %-7d  %-20s  %s%s%s\n", p.PID, p.PPID, startedString(p.StartedAt), cols.row(p), prefix, truncate(p.Command, width))
	for _, child := range node.Children {
		printProcessTreeNode(child, depth+1, cols)
	}
}

func printProcessFDBreakdownRow(p process.Info, thr string, cols processClassColumns) {
	b := p.FDBreakdown
	if b == nil {
		b = &process.FDBreakdown{}
	}
	fds := processFDString(p)
	fmt.Printf("%-7d  %-6s  %-8s  %-5s  %-5d  %-5d  %-5d  %-5d  %-5d  %-5d  %-5d  %s%s\n",
		p.PID, thr, humanSizeKiB(p.RSSKiB), fds, b.PTY, b.Socket, b.Regular, b.Pipe, b.Char, b.Kqueue, b.Other, cols.row(p), truncate(p.Command, 40))
}

func printProcessDeepRow(p process.Info, thr, cpu string, cols processClassColumns) {
	fmt.Printf("%-7d  %-6s  %-10s  %-10s  %-5s  %s%s\n",
		p.PID, thr, humanSizeKiB(p.RSSKiB), cpu, processFDString(p), cols.row(p), truncate(p.Command, 45))
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

func humanSizeBytes(bytes uint64) string {
	if bytes == 0 {
		return "-"
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case bytes >= gib:
		return fmt.Sprintf("%.1fG", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%.1fM", float64(bytes)/mib)
	case bytes >= kib:
		return fmt.Sprintf("%dK", bytes/kib)
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

func percentString(v float64) string {
	if v <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", v)
}

func startedString(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}
