package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/process"
)

func runLineage(args []string) int {
	return runLineageWithIO(args, os.Stdout, os.Stderr, process.SystemCollector{})
}

func runLineageWithIO(args []string, stdout, stderr io.Writer, collector process.Collector) int {
	fs := flag.NewFlagSet("spectra lineage", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human tree")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra lineage [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	res, err := lineage.Build(collector.Collect(context.Background(), process.CollectOptions{}), pid)
	if err != nil {
		fmt.Fprintf(stderr, "lineage: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := printLineage(stdout, res); err != nil {
		fmt.Fprintf(stderr, "lineage: write output: %v\n", err)
		return 1
	}
	return 0
}

func printLineage(w io.Writer, r *lineage.Result) error {
	ew := &errWriter{w: w}
	fmt.Fprintf(ew, "Ancestors of pid %d (nearest first):\n", r.PID)
	for _, a := range r.Ancestors {
		start := "-"
		if !a.StartTime.IsZero() {
			start = a.StartTime.Format(time.DateTime)
		}
		fmt.Fprintf(ew, "  %6d  %-12s  %s  %s\n", a.PID, a.User, start, a.Command)
	}
	if len(r.Flags) > 0 {
		fmt.Fprintf(ew, "Flags: %s (heuristic: per-user launchd agents also have PPID 1)\n", strings.Join(r.Flags, ", "))
	}
	fmt.Fprintln(ew, "Descendants:")
	printLineageNode(ew, r.Tree, 1)
	fmt.Fprintf(ew, "Subtree: %d processes, %d KiB RSS, %.1f%% CPU\n", r.Tree.Count, r.Tree.SubtreeRSSKiB, r.Tree.SubtreeCPUPct)
	return ew.err
}

// errWriter latches the first write error so the printers can stay linear.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err := e.w.Write(p)
	e.err = err
	return n, err
}

func printLineageNode(w io.Writer, n *lineage.Node, depth int) {
	flags := ""
	if len(n.Flags) > 0 {
		flags = " [" + strings.Join(n.Flags, ",") + "]"
	}
	fmt.Fprintf(w, "%s%s (%d)  rss %d KiB  cpu %.1f%%%s\n", strings.Repeat("  ", depth), n.Command, n.PID, n.RSSKiB, n.CPUPct, flags)
	for _, c := range n.Children {
		printLineageNode(w, c, depth+1)
	}
}
