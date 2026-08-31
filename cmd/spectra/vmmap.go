package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/vmmap"
)

const vmmapBin = "/usr/bin/vmmap"

// vmmapRunner runs a command and returns its combined output.
type vmmapRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runVmmap(args []string) int {
	return runVmmapWithIO(args, os.Stdout, os.Stderr, defaultVmmapRunner)
}

func runVmmapWithIO(args []string, stdout, stderr io.Writer, runner vmmapRunner) int {
	fs := flag.NewFlagSet("spectra vmmap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", 8, "Show the top N region types by dirty size (0 for all)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *top < 0 {
		fmt.Fprintln(stderr, "vmmap: --top must be >= 0 (0 means all regions)")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra vmmap [--top <n>] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	out, err := runner(context.Background(), vmmapBin, "--summary", strconv.Itoa(pid))
	if err != nil {
		if isVmmapPermission(string(out)) {
			fmt.Fprintf(stderr, "vmmap: not permitted for PID %d — it likely belongs to another user; re-run as root (sudo)\n", pid)
			return 1
		}
		fmt.Fprintf(stderr, "vmmap failed for PID %d: %v\n", pid, err)
		return 1
	}
	if isVmmapPermission(string(out)) {
		fmt.Fprintf(stderr, "vmmap: not permitted for PID %d — it likely belongs to another user; re-run as root (sudo)\n", pid)
		return 1
	}

	summary := vmmap.Parse(string(out), *top)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}
	printVmmap(stdout, pid, summary)
	return 0
}

func isVmmapPermission(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not permitted") || strings.Contains(l, "unable to access") ||
		strings.Contains(l, "resource must be") || strings.Contains(l, "only root")
}

func printVmmap(w io.Writer, pid int, s vmmap.Summary) {
	fmt.Fprintf(w, "=== vmmap composition (pid %d) ===\n", pid)
	fmt.Fprintf(w, "footprint: %s", humanSize(s.FootprintBytes))
	if s.FootprintPeakBytes > 0 {
		fmt.Fprintf(w, " (peak %s)", humanSize(s.FootprintPeakBytes))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-30s %10s %10s %10s %10s\n", "REGION", "VIRTUAL", "RESIDENT", "DIRTY", "SWAPPED")
	for _, r := range s.Regions {
		fmt.Fprintf(w, "  %-30s %10s %10s %10s %10s\n",
			truncate(r.Type, 30), humanSize(r.VirtualBytes), humanSize(r.ResidentBytes),
			humanSize(r.DirtyBytes), humanSize(r.SwappedBytes))
	}
	fmt.Fprintf(w, "  %-30s %10s %10s %10s %10s\n", "TOTAL",
		humanSize(s.TotalVirtualBytes), humanSize(s.TotalResidentBytes),
		humanSize(s.TotalDirtyBytes), humanSize(s.TotalSwappedBytes))
}

func defaultVmmapRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		// Return out too: the caller inspects it to detect a permission failure.
		return out, fmt.Errorf("vmmap --summary: %w", err)
	}
	return out, nil
}
