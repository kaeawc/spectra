package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
)

func runSample(args []string) int {
	fs := flag.NewFlagSet("spectra sample", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	duration := fs.Int("duration", 1, "Sample duration in seconds")
	interval := fs.Int("interval", 10, "Sampling interval in milliseconds")
	noCache := fs.Bool("no-cache", false, "Do not store the sample in the blob cache")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra sample [--duration <s>] [--interval <ms>] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(os.Stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	// Run: sample <pid> <duration> <interval> -e (stderr only for errors)
	// stdout receives the formatted call tree.
	// #nosec G204 -- PID, duration, and interval are parsed integers.
	cmd := exec.Command("sample",
		strconv.Itoa(pid),
		strconv.Itoa(*duration),
		strconv.Itoa(*interval),
	)
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sample failed for PID %d: %v\n", pid, err)
		return 1
	}

	if !*noCache && cacheStores != nil && cacheStores.Samples != nil {
		key := cache.Key([]byte(fmt.Sprintf("sample:%d:%d", pid, time.Now().UnixNano())))
		if putErr := cacheStores.Samples.Put(key, data); putErr == nil {
			fmt.Fprintf(os.Stderr, "cached as samples/%x\n", key[:4])
		}
	}

	os.Stdout.Write(data)
	return 0
}
