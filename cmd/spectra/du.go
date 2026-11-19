package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/kaeawc/spectra/internal/diskusage"
)

func runDU(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runDUContext(ctx, args, os.Stdout, os.Stderr)
}

func runDUContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spectra du", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", 15, "Show the top N directories and files")
	depth := fs.Int("depth", 0, "Deepest directory level to report (0 = unlimited; totals always include everything)")
	minSize := fs.String("min-size", "", "Omit entries smaller than this allocated size (e.g. 100M)")
	cross := fs.Bool("cross-mounts", false, "Descend into other filesystems")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *top < 1 || *depth < 0 {
		fmt.Fprintln(stderr, "usage: spectra du [--top N] [--depth N] [--min-size SIZE] [--cross-mounts] [--json] <path>")
		return 2
	}
	minBytes, err := parseProcessSize(*minSize)
	if err != nil {
		fmt.Fprintf(stderr, "du: invalid --min-size %q: %v\n", *minSize, err)
		return 2
	}

	res, err := diskusage.Walk(ctx, fs.Arg(0), diskusage.Options{
		Top: *top, Depth: *depth, MinSize: minBytes, CrossMounts: *cross,
	})
	if err != nil {
		fmt.Fprintf(stderr, "du: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(stderr, "du: %v\n", err)
			return 1
		}
		return 0
	}
	printDU(stdout, res)
	return 0
}

func printDU(w io.Writer, r *diskusage.Result) {
	fmt.Fprintf(w, "%s: %s allocated, %d files, %d directories, %dms\n",
		r.Root, humanSizeBytes(r.TotalBytes), r.FileCount, r.DirCount, r.ElapsedMS)
	if r.Partial {
		fmt.Fprintln(w, "PARTIAL: interrupted before the walk finished")
	}
	fmt.Fprintln(w, "\nTop directories (cumulative):")
	for _, d := range r.Dirs {
		fmt.Fprintf(w, "  %8s  %s\n", humanSizeBytes(d.AllocatedBytes), d.Path)
	}
	fmt.Fprintln(w, "\nTop files:")
	for _, f := range r.Files {
		note := ""
		if f.ApparentBytes != 0 {
			note = fmt.Sprintf("  (apparent %s)", humanSizeBytes(f.ApparentBytes))
		}
		fmt.Fprintf(w, "  %8s  %s%s\n", humanSizeBytes(f.AllocatedBytes), f.Path, note)
	}
	if len(r.Unreadable) > 0 {
		fmt.Fprintf(w, "\nUnreadable (%d):\n", len(r.Unreadable))
		for _, u := range r.Unreadable {
			fmt.Fprintf(w, "  %s: %s\n", u.Path, u.Error)
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "\n%d directories skipped (other filesystems or already visited); use --cross-mounts to descend into other filesystems\n", len(r.Skipped))
	}
}
