package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/hangcapture"
	"github.com/kaeawc/spectra/internal/process"
)

// hangCapturer captures a `sample` report for a pid over a duration.
type hangCapturer func(ctx context.Context, pid, durationSec int) (string, error)

// hangDeps injects capture and filesystem access for testing.
type hangDeps struct {
	capture  hangCapturer
	readFile func(string) ([]byte, error)
}

func runHang(args []string) int {
	deps := hangDeps{capture: defaultHangCapture, readFile: os.ReadFile}
	return runHangWithIO(args, os.Stdout, os.Stderr, deps)
}

func runHangWithIO(args []string, stdout, stderr io.Writer, deps hangDeps) int {
	fs := flag.NewFlagSet("spectra hang", flag.ContinueOnError)
	fs.SetOutput(stderr)
	input := fs.String("input", "", "Analyze an existing sample report file instead of capturing")
	duration := fs.Int("duration", 2, "Capture duration in seconds")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var report string
	if *input != "" {
		if fs.NArg() != 0 {
			fmt.Fprintln(stderr, "hang: pass either --input or a pid, not both")
			return 2
		}
		data, err := deps.readFile(*input)
		if err != nil {
			fmt.Fprintf(stderr, "hang: read %s: %v\n", *input, err)
			return 1
		}
		report = string(data)
	} else {
		if fs.NArg() != 1 {
			fmt.Fprintln(stderr, "usage: spectra hang [--duration <s>] [--json] <pid>   (or --input <file>)")
			return 2
		}
		pid, err := strconv.Atoi(fs.Arg(0))
		if err != nil || pid <= 0 {
			fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
			return 2
		}
		report, err = deps.capture(context.Background(), pid, *duration)
		if err != nil {
			fmt.Fprintf(stderr, "hang: capture failed for PID %d: %v\n", pid, err)
			return 1
		}
	}

	analysis := hangcapture.Analyze(report)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(analysis)
		return exitForVerdict(analysis)
	}
	printHang(stdout, analysis)
	return exitForVerdict(analysis)
}

// exitForVerdict returns non-zero when the main thread is genuinely hung, so a
// script can gate on it; idle/unknown return 0.
func exitForVerdict(a hangcapture.Analysis) int {
	switch a.MainThread.Verdict {
	case hangcapture.VerdictLockBlocked, hangcapture.VerdictIOBlocked, hangcapture.VerdictSpinning:
		return 3
	default:
		return 0
	}
}

func printHang(w io.Writer, a hangcapture.Analysis) {
	mt := a.MainThread
	fmt.Fprintf(w, "=== hang analysis ===\n")
	fmt.Fprintf(w, "  main thread: %s\n", mt.Verdict)
	fmt.Fprintf(w, "  %s\n", mt.Reason)
	if len(mt.TopFrames) > 0 {
		fmt.Fprintln(w, "  stack (leaf last):")
		for _, f := range mt.TopFrames {
			fmt.Fprintf(w, "    %s\n", truncate(f, 76))
		}
	}
}

func defaultHangCapture(ctx context.Context, pid, durationSec int) (string, error) {
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
