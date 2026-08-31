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

	"github.com/kaeawc/spectra/internal/spindump"
)

// Absolute paths to the macOS utilities, so PATH cannot substitute them.
const (
	spindumpBin = "/usr/sbin/spindump"
	sudoBin     = "/usr/bin/sudo"
)

// spindumpRunner runs a command and returns its combined output.
type spindumpRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// spindumpDeps injects command execution and filesystem access so the command
// is testable without a real capture or real files.
type spindumpDeps struct {
	run       spindumpRunner
	readFile  func(path string) ([]byte, error)
	writeFile func(path string, data []byte, perm os.FileMode) error
}

func runSpindump(args []string) int {
	deps := spindumpDeps{
		run:       defaultSpindumpRunner,
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
	}
	return runSpindumpWithIO(args, os.Stdout, os.Stderr, deps)
}

func runSpindumpWithIO(args []string, stdout, stderr io.Writer, deps spindumpDeps) int {
	fs := flag.NewFlagSet("spectra spindump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "Parse an existing spindump report file instead of capturing")
	duration := fs.Int("duration", 2, "Capture duration in seconds")
	sudo := fs.Bool("sudo", false, "Prepend sudo (spindump requires root to sample a live process)")
	out := fs.String("out", "", "Also write the raw report to this file")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var report string
	if *input != "" {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "spindump: pass either --input or a pid, not both")
			return 2
		}
		if *out != "" {
			fmt.Fprintln(stderr, "spindump: --out has no effect with --input (the report already exists)")
			return 2
		}
		data, err := deps.readFile(*input)
		if err != nil {
			fmt.Fprintf(stderr, "spindump: read %s: %v\n", *input, err)
			return 1
		}
		report = string(data)
	} else {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: spectra spindump [--duration <s>] [--sudo] [--out <file>] <pid>   (or --input <file>)")
			return 2
		}
		pid, err := strconv.Atoi(fs.Arg(0))
		if err != nil || pid <= 0 {
			fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
			return 2
		}
		report, err = captureSpindump(deps.run, pid, *duration, *sudo)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
		if *out != "" {
			if err := deps.writeFile(*out, []byte(report), 0o600); err != nil {
				fmt.Fprintf(stderr, "spindump: write %s: %v\n", *out, err)
				return 1
			}
		}
	}

	parsed := spindump.Parse(report)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(parsed)
		return 0
	}
	printSpindump(stdout, parsed)
	return 0
}

func captureSpindump(runner spindumpRunner, pid, duration int, sudo bool) (string, error) {
	name := spindumpBin
	args := []string{strconv.Itoa(pid), strconv.Itoa(duration), "-stdout"}
	if sudo {
		args = append([]string{name}, args...)
		name = sudoBin
	}
	out, err := runner(context.Background(), name, args...)
	if err != nil {
		if strings.Contains(string(out), "must be run as root") || strings.Contains(err.Error(), "must be run as root") {
			return "", fmt.Errorf("spindump requires root — re-run with --sudo (or as root)")
		}
		return "", fmt.Errorf("spindump capture failed: %w", err)
	}
	if strings.Contains(string(out), "must be run as root") {
		return "", fmt.Errorf("spindump requires root — re-run with --sudo (or as root)")
	}
	return string(out), nil
}

func printSpindump(w io.Writer, r spindump.Report) {
	fmt.Fprint(w, "=== spindump summary ===\n")
	if r.Duration != "" {
		fmt.Fprintf(w, "duration: %s\n", r.Duration)
	}
	if len(r.Processes) == 0 {
		fmt.Fprintln(w, "  (no processes parsed)")
		return
	}
	for _, p := range r.Processes {
		fmt.Fprintf(w, "  %s [%d]\n", p.Name, p.PID)
		for _, f := range p.Heaviest {
			fmt.Fprintf(w, "    %6d  %s\n", f.Samples, truncate(f.Symbol, 72))
		}
	}
}

func defaultSpindumpRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
