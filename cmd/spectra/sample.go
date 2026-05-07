package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/cache"
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

	var store process.SampleStore
	if !*noCache && cacheStores != nil && cacheStores.Samples != nil {
		store = sampleCacheStore{store: cacheStores.Samples}
	}
	sampler := process.NewSampler(nil, store)
	result, err := sampler.Capture(context.Background(), process.SampleOptions{
		PID:        pid,
		Duration:   time.Duration(*duration) * time.Second,
		IntervalMS: *interval,
		Store:      store != nil,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sample failed for PID %d: %v\n", pid, err)
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

	fmt.Fprint(os.Stdout, result.Output)
	return 0
}
