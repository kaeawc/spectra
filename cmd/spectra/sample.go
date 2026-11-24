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

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/nativeprofile"
	"github.com/kaeawc/spectra/internal/process"
)

type sampleCacheStore struct {
	store *cache.ShardedStore
}

func (s sampleCacheStore) PutSample(_ context.Context, sample process.SampleResult) error {
	key := cache.Key([]byte(fmt.Sprintf("sample:%d:%d", sample.PID, sample.TakenAt.UnixNano())))
	if err := s.store.Put(key, []byte(sample.Output)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "cached as samples/%x\n", key[:4])
	return nil
}

type sampleDeps struct {
	capture func(ctx context.Context, opts process.SampleOptions) (process.SampleResult, error)
}

func runSample(args []string) int {
	return runSampleWithIO(args, os.Stdout, os.Stderr, sampleDeps{capture: sampleCaptureWithCache})
}

func sampleCaptureWithCache(ctx context.Context, opts process.SampleOptions) (process.SampleResult, error) {
	var store process.SampleStore
	if opts.Store && cacheStores != nil && cacheStores.Samples != nil {
		store = sampleCacheStore{store: cacheStores.Samples}
	}
	return process.NewSampler(nil, store).Capture(ctx, opts)
}

func runSampleWithIO(args []string, stdout, stderr io.Writer, deps sampleDeps) int {
	fs := flag.NewFlagSet("spectra sample", flag.ContinueOnError)
	fs.SetOutput(stderr)
	duration := fs.Int("duration", 1, "Sample duration in seconds")
	interval := fs.Int("interval", 10, "Sampling interval in milliseconds")
	noCache := fs.Bool("no-cache", false, "Do not store the sample in the blob cache")
	asJSON := fs.Bool("json", false, "Emit structured JSON (top self-time stacks, main-thread verdict) instead of the raw report")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra sample [--duration <s>] [--interval <ms>] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	result, report, err := nativeprofile.Sample(context.Background(), deps.capture, process.SampleOptions{
		PID:        pid,
		Duration:   time.Duration(*duration) * time.Second,
		IntervalMS: *interval,
		Store:      !*noCache,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	recordArtifactCLI(artifact.Record{
		Kind:        artifact.KindProcessSample,
		Sensitivity: artifact.SensitivityMedium,
		Source:      "cli",
		Command:     "spectra sample",
		CacheKind:   cache.KindSamples,
		PID:         pid,
		SizeBytes:   int64(len(result.Output)),
		Metadata: map[string]string{
			"duration": strconv.Itoa(*duration),
			"interval": strconv.Itoa(*interval),
		},
	})

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(stderr, "sample: encode: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, result.Output)
	return 0
}
