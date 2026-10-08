package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/heap"
)

// runJVMDominators computes retained sizes over a saved .hprof heap dump and
// ranks the objects retaining the most memory — the leak suspects a class
// histogram (shallow size) cannot find. It is equivalent to
// `jvm heap-hprof --retained`.
func runJVMDominators(args []string) int {
	fs := flag.NewFlagSet("spectra jvm dominators", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit the retained-size analysis as JSON")
	top := fs.Int("top", 20, "Number of top retained-size objects (leak suspects) to rank")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *top < 0 {
		fmt.Fprintln(os.Stderr, "dominators: --top must be >= 0")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra jvm dominators [--top N] [--json] <file.hprof>")
		return 2
	}
	return runRetainedAnalysis(fs.Arg(0), *top, *asJSON)
}

// runRetainedAnalysis parses path's object graph, builds the dominator tree,
// and prints the top objects and classes by retained size.
func runRetainedAnalysis(path string, limit int, asJSON bool) int {
	graph, ok := loadObjectGraph(path)
	if !ok {
		return 1
	}
	res := heap.RankRetained(heap.ComputeRetained(graph), limit)
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return 0
	}
	printDominators(os.Stdout, path, res)
	return 0
}

func printDominators(w io.Writer, path string, res heap.DominatorResult) {
	fmt.Fprintf(w, "=== retained-size analysis: %s ===\n", path)
	fmt.Fprintf(w, "total heap (shallow): %s | reachable: %s across %d objects\n",
		humanSize(res.TotalShallowBytes), humanSize(res.ReachableBytes), res.ReachableObjects)
	if len(res.Classes) > 0 {
		fmt.Fprintln(w, "\nTop classes by retained size:")
		fmt.Fprintf(w, "  %12s  %7s  %12s  %10s  %s\n", "RETAINED", "%HEAP", "SHALLOW", "INSTANCES", "CLASS")
		for _, c := range res.Classes {
			fmt.Fprintf(w, "  %12s  %6.1f%%  %12s  %10d  %s\n",
				humanSize(c.RetainedBytes), c.PercentOfHeap, humanSize(c.ShallowBytes), c.Instances, truncate(c.ClassName, 60))
		}
	}
	fmt.Fprintln(w, "\nTop objects by retained size:")
	fmt.Fprintf(w, "  %12s  %7s  %-40s\n", "RETAINED", "%HEAP", "CLASS (object id)")
	for _, s := range res.Suspects {
		fmt.Fprintf(w, "  %12s  %6.1f%%  %s\n",
			humanSize(s.RetainedBytes), s.PercentOfHeap,
			truncate(fmt.Sprintf("%s (0x%x)", s.ClassName, s.ID), 72))
	}
}
