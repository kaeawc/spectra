package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/binaryimages"
	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/flamegraph"
	"github.com/kaeawc/spectra/internal/hangcapture"
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

// sampleTopStacks caps the stacks emitted in JSON; stack_count carries the
// full number so a consumer knows when the list is truncated.
const sampleTopStacks = 50

type sampleStack struct {
	Frames       []string                `json:"frames"`
	Count        int                     `json:"count"`
	Percent      float64                 `json:"percent"`
	FrameDetails []binaryimages.Location `json:"frame_details,omitempty"`
}

// sampleReport is the structured form of one sample capture. The raw text
// report is deleted after capture, so it is not referenced here.
type sampleReport struct {
	PID          int                    `json:"pid"`
	Process      string                 `json:"process,omitempty"`
	TakenAt      time.Time              `json:"taken_at"`
	DurationMS   int64                  `json:"duration_ms"`
	IntervalMS   int                    `json:"interval_ms"`
	TotalSamples int                    `json:"total_samples"`
	StackCount   int                    `json:"stack_count"`
	Stacks       []sampleStack          `json:"stacks"`
	MainThread   hangcapture.MainThread `json:"main_thread"`
}

var sampleProcessLine = regexp.MustCompile(`(?m)^Process:\s+(.+?)\s+\[\d+\]\s*$`)

// buildSampleReport shapes a captured report using the existing flamegraph
// folder (self-time stacks) and hangcapture main-thread analysis.
func buildSampleReport(res process.SampleResult) sampleReport {
	folded := flamegraph.Fold(res.Output)
	sort.SliceStable(folded, func(i, j int) bool { return folded[i].Count > folded[j].Count })
	total := 0
	for _, f := range folded {
		total += f.Count
	}
	rep := sampleReport{
		PID:          res.PID,
		TakenAt:      res.TakenAt,
		DurationMS:   res.DurationMS,
		IntervalMS:   res.IntervalMS,
		TotalSamples: total,
		StackCount:   len(folded),
		Stacks:       []sampleStack{},
		MainThread:   hangcapture.Analyze(res.Output).MainThread,
	}
	if m := sampleProcessLine.FindStringSubmatch(res.Output); m != nil {
		rep.Process = strings.TrimSpace(m[1])
	}
	for i, f := range folded {
		if i >= sampleTopStacks {
			break
		}
		pct := 0.0
		if total > 0 {
			pct = float64(int(float64(f.Count)/float64(total)*10000+0.5)) / 100
		}
		rep.Stacks = append(rep.Stacks, sampleStack{Frames: f.Frames, Count: f.Count, Percent: pct, FrameDetails: f.FrameDetails})
	}
	return rep
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

	result, err := deps.capture(context.Background(), process.SampleOptions{
		PID:        pid,
		Duration:   time.Duration(*duration) * time.Second,
		IntervalMS: *interval,
		Store:      !*noCache,
	})
	if err != nil {
		fmt.Fprintf(stderr, "sample failed for PID %d: %v\n", pid, err)
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
		if err := enc.Encode(buildSampleReport(result)); err != nil {
			fmt.Fprintf(stderr, "sample: encode: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, result.Output)
	return 0
}
