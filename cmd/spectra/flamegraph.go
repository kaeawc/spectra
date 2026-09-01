package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/flamegraph"
	"github.com/kaeawc/spectra/internal/process"
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
		capture:   defaultSampleCapture,
		readFile:  os.ReadFile,
		writeFile: os.WriteFile,
	}
	return runFlamegraphWithIO(args, os.Stdout, os.Stderr, deps)
}

func runFlamegraphWithIO(args []string, stdout, stderr io.Writer, deps flamegraphDeps) int {
	fs := flag.NewFlagSet("spectra flamegraph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "Fold an existing sample report file instead of capturing")
	duration := fs.Int("duration", 2, "Capture duration in seconds")
	out := fs.String("out", "", "Write the SVG here (default <pid>.flamegraph.svg)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var report, title, outPath string
	if *input != "" {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "flamegraph: pass either --input or a pid, not both")
			return 2
		}
		data, err := deps.readFile(*input)
		if err != nil {
			fmt.Fprintf(stderr, "flamegraph: read %s: %v\n", *input, err)
			return 1
		}
		report, title, outPath = string(data), "flamegraph: "+*input, *out
		if outPath == "" {
			outPath = *input + ".flamegraph.svg"
		}
	} else {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: spectra flamegraph [--duration <s>] [--out <file>] <pid>   (or --input <file>)")
			return 2
		}
		pid, err := strconv.Atoi(fs.Arg(0))
		if err != nil || pid <= 0 {
			fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
			return 2
		}
		report, err = deps.capture(context.Background(), pid, *duration)
		if err != nil {
			fmt.Fprintf(stderr, "flamegraph: capture failed for PID %d: %v\n", pid, err)
			return 1
		}
		title = fmt.Sprintf("flamegraph: pid %d (%ds)", pid, *duration)
		outPath = *out
		if outPath == "" {
			outPath = fmt.Sprintf("%d.flamegraph.svg", pid)
		}
	}

	folded := flamegraph.Fold(report)
	if len(folded) == 0 {
		fmt.Fprintln(stderr, "flamegraph: no stacks found in the sample (was it a Call graph report?)")
		return 1
	}
	svg := flamegraph.RenderSVG(folded, title)
	if err := deps.writeFile(outPath, []byte(svg), 0o644); err != nil {
		fmt.Fprintf(stderr, "flamegraph: write %s: %v\n", outPath, err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (%d folded stacks)\n", outPath, len(folded))
	return 0
}

func defaultSampleCapture(ctx context.Context, pid, durationSec int) (string, error) {
	sampler := process.NewSampler(nil, nil)
	res, err := sampler.Capture(ctx, process.SampleOptions{
		PID:      pid,
		Duration: time.Duration(durationSec) * time.Second,
	})
	if err != nil {
		return "", err
	}
	return res.Output, nil
}
