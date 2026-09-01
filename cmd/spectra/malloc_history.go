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

	"github.com/kaeawc/spectra/internal/mallochistory"
)

const (
	mallocHistoryBin = "/usr/bin/malloc_history"
	mallocSudoBin    = "/usr/bin/sudo"
)

// mallocHistoryRunner runs a command and returns its combined output.
type mallocHistoryRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// mallocHistoryResult is the JSON shape for the command.
type mallocHistoryResult struct {
	PID     int                       `json:"pid"`
	Address string                    `json:"address,omitempty"`
	Sites   []mallochistory.AllocSite `json:"sites,omitempty"`
	Traces  []mallochistory.Backtrace `json:"traces,omitempty"`
}

func runMallocHistory(args []string) int {
	return runMallocHistoryWithIO(args, os.Stdout, os.Stderr, defaultMallocHistoryRunner)
}

func runMallocHistoryWithIO(args []string, stdout, stderr io.Writer, runner mallocHistoryRunner) int {
	fs := flag.NewFlagSet("spectra malloc-history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	address := fs.String("address", "", "Report the allocation backtrace for this heap address (hex)")
	top := fs.Int("top", 10, "Show the top N allocation sites by bytes (0 for all)")
	sudo := fs.Bool("sudo", false, "Prepend sudo (malloc_history needs root for another user's process)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *top < 0 {
		fmt.Fprintln(stderr, "malloc-history: --top must be >= 0")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra malloc-history [--address <hex>] [--top <n>] [--sudo] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	if *address != "" && !isHexAddr(*address) {
		fmt.Fprintf(stderr, "malloc-history: invalid address %q (want hex like 0x600000abcdef)\n", *address)
		return 2
	}

	// malloc_history takes the pid first, then the mode/address:
	//   malloc_history <pid> -allBySize
	//   malloc_history <pid> <address>
	name := mallocHistoryBin
	cmdArgs := []string{strconv.Itoa(pid)}
	if *address != "" {
		cmdArgs = append(cmdArgs, *address)
	} else {
		cmdArgs = append(cmdArgs, "-allBySize")
	}
	if *sudo {
		cmdArgs = append([]string{name}, cmdArgs...)
		name = mallocSudoBin
	}

	out, runErr := runner(context.Background(), name, cmdArgs...)
	if mallochistory.StackLoggingDisabled(string(out)) {
		fmt.Fprintf(stderr, "malloc-history: PID %d has no malloc stack logging — relaunch the target with MallocStackLogging=1 in its environment\n", pid)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "malloc-history failed for PID %d: %v\n", pid, runErr)
		return 1
	}

	result := mallocHistoryResult{PID: pid, Address: *address}
	if *address != "" {
		result.Traces = mallochistory.ParseAddress(string(out))
	} else {
		result.Sites = mallochistory.ParseAllBySize(string(out), *top)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printMallocHistory(stdout, result)
	return 0
}

func printMallocHistory(w io.Writer, r mallocHistoryResult) {
	if r.Address != "" {
		fmt.Fprintf(w, "=== malloc-history pid %d @ %s ===\n", r.PID, r.Address)
		if len(r.Traces) == 0 {
			fmt.Fprintln(w, "  (no backtraces for this address)")
		}
		for _, t := range r.Traces {
			fmt.Fprintf(w, "  %s: %s\n", t.Kind, strings.Join(t.Frames, " ← "))
		}
		return
	}
	fmt.Fprintf(w, "=== malloc-history pid %d — top allocation sites ===\n", r.PID)
	if len(r.Sites) == 0 {
		fmt.Fprintln(w, "  (no allocation sites parsed)")
	}
	for _, s := range r.Sites {
		fmt.Fprintf(w, "  %s in %d calls  %s\n", humanSize(s.Bytes), s.Calls, truncate(leaf(s.Stack), 64))
	}
}

func leaf(stack []string) string {
	if len(stack) == 0 {
		return ""
	}
	return stack[len(stack)-1]
}

func isHexAddr(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 16, 64)
	return err == nil
}

func defaultMallocHistoryRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		// Return out too: the caller inspects it for the stack-logging notice.
		return out, fmt.Errorf("malloc_history: %w", err)
	}
	return out, nil
}
