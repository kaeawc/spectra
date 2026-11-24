package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/kaeawc/spectra/internal/nativeprofile"
)

// sampleCapturer captures a `sample` report for a pid over a duration.
type sampleCapturer func(ctx context.Context, pid, durationSec int) (string, error)

// flamegraphDeps injects capture and filesystem access for testing.
type flamegraphDeps struct {
	capture   sampleCapturer
	readFile  func(string) ([]byte, error)
	writeFile func(string, []byte, os.FileMode) error
}

func runFlamegraph(args []string) int {
	deps := flamegraphDeps{
		capture:   nativeprofile.HostSampleCapture,
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
	}
	return runFlamegraphWithIO(args, os.Stdout, os.Stderr, deps)
}

func runFlamegraphWithIO(args []string, stdout, stderr io.Writer, deps flamegraphDeps) int {
	fs := flag.NewFlagSet("spectra flamegraph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "Fold an existing sample report file instead of capturing")
	duration := fs.Int("duration", int(nativeprofile.DefaultFlamegraphDuration.Seconds()), "Capture duration in seconds")
	out := fs.String("out", "", "Write the SVG here (default <pid>.flamegraph.svg)")
	asJSON := fs.Bool("json", false, "After writing the SVG, print its folded stacks (with frame image details) as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := nativeprofile.FlamegraphOptions{
		Input: *input, ReadFile: deps.readFile,
		DurationSec: *duration, Capture: deps.capture,
		Out: *out, WriteFile: deps.writeFile,
	}
	if *input != "" {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "flamegraph: pass either --input or a pid, not both")
			return 2
		}
	} else {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: spectra flamegraph [--duration <s>] [--out <file>] [--json] <pid>   (or --input <file>)")
			return 2
		}
		pid, err := strconv.Atoi(fs.Arg(0))
		if err != nil || pid <= 0 {
			fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
			return 2
		}
		opts.PID = pid
	}

	rep, err := nativeprofile.Flamegraph(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(stderr, "flamegraph: encode: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "wrote %s (%d folded stacks)\n", rep.SVG, rep.StackCount)
	return 0
}
