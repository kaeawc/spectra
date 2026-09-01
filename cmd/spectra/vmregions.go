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

	"github.com/kaeawc/spectra/internal/vmregions"
)

const vmregionsBin = "/usr/bin/vmmap"

// vmregionsRunner runs a command and returns its combined output.
type vmregionsRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runVmregions(args []string) int {
	return runVmregionsWithIO(args, os.Stdout, os.Stderr, defaultVmregionsRunner)
}

func runVmregionsWithIO(args []string, stdout, stderr io.Writer, runner vmregionsRunner) int {
	fs := flag.NewFlagSet("spectra vmregions", flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.Int("top", 8, "Show the top N regions by dirty size (0 for all)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *top < 0 {
		fmt.Fprintln(stderr, "vmregions: --top must be >= 0")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra vmregions [--top <n>] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	out, runErr := runner(context.Background(), vmregionsBin, strconv.Itoa(pid))
	if isVmregionsPermission(string(out)) {
		fmt.Fprintf(stderr, "vmregions: not permitted for PID %d — it likely belongs to another user; re-run as root (sudo)\n", pid)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "vmregions failed for PID %d: %v\n", pid, runErr)
		return 1
	}

	comp := vmregions.Parse(string(out), *top)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(comp)
		return 0
	}
	printVmregions(stdout, pid, comp)
	return 0
}

func isVmregionsPermission(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "not permitted") || strings.Contains(l, "unable to access") ||
		strings.Contains(l, "resource must be") || strings.Contains(l, "only root")
}

func printVmregions(w io.Writer, pid int, c vmregions.Composition) {
	fmt.Fprintf(w, "=== vmregions composition (pid %d) ===\n", pid)
	fmt.Fprintf(w, "  resident: %s   dirty: %s\n", humanSize(c.TotalResidentBytes), humanSize(c.TotalDirtyBytes))
	fmt.Fprintf(w, "  shared-resident: %s\n", humanSize(c.SharedResidentBytes))
	fmt.Fprintf(w, "  dirty: file-backed %s | anonymous %s\n", humanSize(c.FileBackedDirtyBytes), humanSize(c.AnonymousDirtyBytes))
	fmt.Fprintf(w, "  resident: writable %s | executable %s\n", humanSize(c.WritableResidentBytes), humanSize(c.ExecutableResidentBytes))
	if len(c.RWXRegions) > 0 {
		fmt.Fprintf(w, "  ! %d RWX region(s) (writable + executable — W^X violation):\n", len(c.RWXRegions))
		for _, r := range c.RWXRegions {
			fmt.Fprintf(w, "      %s %s-%s %s %s\n", truncate(r.Type, 22), r.AddrStart, r.AddrEnd, r.Prot, truncate(r.Detail, 30))
		}
	}
	fmt.Fprintln(w, "  top regions by dirty:")
	for _, r := range c.TopDirty {
		fmt.Fprintf(w, "    %10s  %-22s %s %s\n", humanSize(r.DirtyBytes), truncate(r.Type, 22), r.Prot, truncate(r.Detail, 28))
	}
}

func defaultVmregionsRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		// Return out too: the caller inspects it for a permission failure.
		return out, fmt.Errorf("vmmap: %w", err)
	}
	return out, nil
}
