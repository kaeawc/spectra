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

	"github.com/kaeawc/spectra/internal/lsmp"
)

const (
	lsmpBin     = "/usr/bin/lsmp"
	lsmpSudoBin = "/usr/bin/sudo"
)

// lsmpRunner runs a command and returns its combined output.
type lsmpRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func runLsmp(args []string) int {
	return runLsmpWithIO(args, os.Stdout, os.Stderr, defaultLsmpRunner)
}

func runLsmpWithIO(args []string, stdout, stderr io.Writer, runner lsmpRunner) int {
	fs := flag.NewFlagSet("spectra lsmp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sudo := fs.Bool("sudo", false, "Prepend sudo (lsmp requires root to read a task's ports)")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra lsmp [--sudo] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	name := lsmpBin
	cmdArgs := []string{"-p", strconv.Itoa(pid)}
	if *sudo {
		cmdArgs = append([]string{name}, cmdArgs...)
		name = lsmpSudoBin
	}
	out, runErr := runner(context.Background(), name, cmdArgs...)
	if isLsmpPermission(string(out)) {
		fmt.Fprintf(stderr, "lsmp: cannot read ports for PID %d — lsmp needs root; re-run with --sudo (or as root)\n", pid)
		return 1
	}
	if runErr != nil {
		fmt.Fprintf(stderr, "lsmp failed for PID %d: %v\n", pid, runErr)
		return 1
	}

	summary := lsmp.Parse(string(out))
	if summary.TotalPorts == 0 {
		fmt.Fprintf(stderr, "lsmp: no ports parsed for PID %d — if the report was empty, lsmp likely needs root (--sudo)\n", pid)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}
	printLsmp(stdout, pid, summary)
	return 0
}

func isLsmpPermission(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "task_for_pid() failed") || strings.Contains(l, "should run as root")
}

func printLsmp(w io.Writer, pid int, s lsmp.Summary) {
	fmt.Fprintf(w, "=== lsmp Mach ports (pid %d) ===\n", pid)
	fmt.Fprintf(w, "  total ports: %d\n", s.TotalPorts)
	fmt.Fprintf(w, "  recv:      %d\n", s.RecvRights)
	fmt.Fprintf(w, "  send:      %d\n", s.SendRights)
	fmt.Fprintf(w, "  send-once: %d\n", s.SendOnceRights)
	fmt.Fprintf(w, "  port-sets: %d\n", s.PortSets)
	for _, n := range s.Notes {
		fmt.Fprintf(w, "  ! %s\n", n)
	}
}

func defaultLsmpRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		// Return out too: the caller inspects it for a permission failure.
		return out, fmt.Errorf("lsmp -p: %w", err)
	}
	return out, nil
}
