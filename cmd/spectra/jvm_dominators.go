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
// histogram (shallow size) cannot find.
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
	path := fs.Arg(0)
	graph, err := heap.ParseObjectGraphFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parsing %q: %v\n", path, err)
		return 1
	}
	res := heap.Dominators(graph, *top)
	if graph.Unresolved > 0 {
		fmt.Fprintf(os.Stderr, "note: %d instance(s) had no class layout; their references were not walked\n", graph.Unresolved)
	}
	if *asJSON {
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
	fmt.Fprintf(w, "  %12s  %7s  %-40s\n", "RETAINED", "%HEAP", "CLASS (object id)")
	for _, s := range res.Suspects {
		fmt.Fprintf(w, "  %12s  %6.1f%%  %s\n",
			humanSize(s.RetainedBytes), s.PercentOfHeap,
			truncate(fmt.Sprintf("%s (0x%x)", s.ClassName, s.ID), 48))
	}
}
