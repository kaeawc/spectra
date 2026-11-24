// Package nativeprofile builds the values `spectra sample|spindump|flamegraph
// --json` print, from a live macOS capture or an existing report, so the CLI
// and the daemon return the same JSON. Error texts are the CLI's.
package nativeprofile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/binaryimages"
	"github.com/kaeawc/spectra/internal/flamegraph"
	"github.com/kaeawc/spectra/internal/hangcapture"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/spindump"
)

// The CLI's flag defaults.
const (
	DefaultSampleDuration     = time.Second
	DefaultSampleIntervalMS   = 10
	DefaultSpindumpDuration   = 2 * time.Second
	DefaultFlamegraphDuration = 2 * time.Second
)

// Features name the captures in unsupported-platform errors, as the CLI's
// macOS-only gate does.
const (
	SampleFeature     = "process sampling (sample(1))"
	SpindumpFeature   = "spindump stack reports"
	FlamegraphFeature = "native flamegraphs (sample(1))"
	// OffDarwinHint points Linux users at the /proc equivalent.
	OffDarwinHint = "on Linux, `spectra procdiag <pid>` diagnoses a stuck or slow process from /proc"
)

// SampleTopStacks caps the stacks emitted in JSON; stack_count carries the
// full number so a consumer knows when the list is truncated.
const SampleTopStacks = 50

// SampleStack is one self-time stack of a sample report.
type SampleStack struct {
	Frames       []string                `json:"frames"`
	Count        int                     `json:"count"`
	Percent      float64                 `json:"percent"`
	FrameDetails []binaryimages.Location `json:"frame_details,omitempty"`
}

// SampleReport is the `spectra sample --json` value. The raw text report is
// deleted after capture, so it is not referenced here.
type SampleReport struct {
	PID          int                    `json:"pid"`
	Process      string                 `json:"process,omitempty"`
	TakenAt      time.Time              `json:"taken_at"`
	DurationMS   int64                  `json:"duration_ms"`
	IntervalMS   int                    `json:"interval_ms"`
	TotalSamples int                    `json:"total_samples"`
	StackCount   int                    `json:"stack_count"`
	Stacks       []SampleStack          `json:"stacks"`
	MainThread   hangcapture.MainThread `json:"main_thread"`
}

var sampleProcessLine = regexp.MustCompile(`(?m)^Process:\s+(.+?)\s+\[\d+\]\s*$`)

// BuildSampleReport shapes a captured report using the flamegraph folder
// (self-time stacks) and hangcapture main-thread analysis.
func BuildSampleReport(res process.SampleResult) SampleReport {
	folded := flamegraph.Fold(res.Output)
	sort.SliceStable(folded, func(i, j int) bool { return folded[i].Count > folded[j].Count })
	total := 0
	for _, f := range folded {
		total += f.Count
	}
	rep := SampleReport{
		PID:          res.PID,
		TakenAt:      res.TakenAt,
		DurationMS:   res.DurationMS,
		IntervalMS:   res.IntervalMS,
		TotalSamples: total,
		StackCount:   len(folded),
		Stacks:       []SampleStack{},
		MainThread:   hangcapture.Analyze(res.Output).MainThread,
	}
	if m := sampleProcessLine.FindStringSubmatch(res.Output); m != nil {
		rep.Process = strings.TrimSpace(m[1])
	}
	for i, f := range folded {
		if i >= SampleTopStacks {
			break
		}
		pct := 0.0
		if total > 0 {
			pct = float64(int(float64(f.Count)/float64(total)*10000+0.5)) / 100
		}
		rep.Stacks = append(rep.Stacks, SampleStack{Frames: f.Frames, Count: f.Count, Percent: pct, FrameDetails: f.FrameDetails})
	}
	return rep
}

// SampleCapture runs one sample(1) capture.
type SampleCapture func(ctx context.Context, opts process.SampleOptions) (process.SampleResult, error)

// Sample captures opts.PID with capture and returns the raw result and the
// --json report.
func Sample(ctx context.Context, capture SampleCapture, opts process.SampleOptions) (process.SampleResult, SampleReport, error) {
	res, err := capture(ctx, opts)
	if err != nil {
		return process.SampleResult{}, SampleReport{}, fmt.Errorf("sample failed for PID %d: %w", opts.PID, err)
	}
	return res, BuildSampleReport(res), nil
}

// Absolute paths to the macOS utilities, so PATH cannot substitute them.
const (
	SpindumpBin = "/usr/sbin/spindump"
	SudoBin     = "/usr/bin/sudo"
)

// Runner runs a command and returns its combined output.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// HostRunner runs a command on the host, killing it when ctx ends.
func HostRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput() // #nosec G204 -- fixed absolute tool paths
}

var errSpindumpRoot = fmt.Errorf("spindump requires root — re-run with --sudo (or as root)")

// CaptureSpindump runs `spindump <pid> <seconds> -stdout`, under sudo when
// asked, and returns the raw report.
func CaptureSpindump(ctx context.Context, run Runner, pid, durationSec int, sudo bool) (string, error) {
	name := SpindumpBin
	args := []string{strconv.Itoa(pid), strconv.Itoa(durationSec), "-stdout"}
	if sudo {
		args = append([]string{name}, args...)
		name = SudoBin
	}
	out, err := run(ctx, name, args...)
	if err != nil {
		if strings.Contains(string(out), "must be run as root") || strings.Contains(err.Error(), "must be run as root") {
			return "", errSpindumpRoot
		}
		return "", fmt.Errorf("spindump capture failed: %w", err)
	}
	if strings.Contains(string(out), "must be run as root") {
		return "", errSpindumpRoot
	}
	return string(out), nil
}

// SpindumpOptions select a spindump capture (PID) or an existing report
// (Input).
type SpindumpOptions struct {
	Input    string
	ReadFile func(string) ([]byte, error)

	PID         int
	DurationSec int
	Sudo        bool
	Run         Runner
}

// Spindump returns the raw report and the `spectra spindump --json` value.
func Spindump(ctx context.Context, opts SpindumpOptions) (string, spindump.Report, error) {
	var raw string
	if opts.Input != "" {
		data, err := opts.ReadFile(opts.Input)
		if err != nil {
			return "", spindump.Report{}, fmt.Errorf("spindump: read %s: %w", opts.Input, err)
		}
		raw = string(data)
	} else {
		var err error
		if raw, err = CaptureSpindump(ctx, opts.Run, opts.PID, opts.DurationSec, opts.Sudo); err != nil {
			return "", spindump.Report{}, err
		}
	}
	return raw, spindump.Parse(raw), nil
}

// FlamegraphReport is the `spectra flamegraph --json` value: the SVG written
// and every folded stack behind it.
type FlamegraphReport struct {
	SVG        string              `json:"svg"`
	StackCount int                 `json:"stack_count"`
	Stacks     []flamegraph.Folded `json:"stacks"`
}

// FlamegraphOptions select a capture (PID) or an existing sample report
// (Input), and where the SVG goes. An empty Out is the CLI's default,
// relative to the working directory.
type FlamegraphOptions struct {
	Input    string
	ReadFile func(string) ([]byte, error)

	PID         int
	DurationSec int
	Capture     func(ctx context.Context, pid, durationSec int) (string, error)

	Out       string
	WriteFile func(string, []byte, os.FileMode) error
}

// Flamegraph folds the sample, writes the SVG to the output path and returns
// the --json value.
func Flamegraph(ctx context.Context, opts FlamegraphOptions) (FlamegraphReport, error) {
	var report, title string
	outPath := opts.Out
	if opts.Input != "" {
		data, err := opts.ReadFile(opts.Input)
		if err != nil {
			return FlamegraphReport{}, fmt.Errorf("flamegraph: read %s: %w", opts.Input, err)
		}
		report, title = string(data), "flamegraph: "+opts.Input
		if outPath == "" {
			outPath = opts.Input + ".flamegraph.svg"
		}
	} else {
		var err error
		if report, err = opts.Capture(ctx, opts.PID, opts.DurationSec); err != nil {
			return FlamegraphReport{}, fmt.Errorf("flamegraph: capture failed for PID %d: %w", opts.PID, err)
		}
		title = fmt.Sprintf("flamegraph: pid %d (%ds)", opts.PID, opts.DurationSec)
		if outPath == "" {
			outPath = fmt.Sprintf("%d.flamegraph.svg", opts.PID)
		}
	}
	folded := flamegraph.Fold(report)
	if len(folded) == 0 {
		return FlamegraphReport{}, fmt.Errorf("flamegraph: no stacks found in the sample (was it a Call graph report?)")
	}
	if err := ctx.Err(); err != nil {
		return FlamegraphReport{}, err
	}
	svg := flamegraph.RenderSVG(folded, title)
	if err := opts.WriteFile(outPath, []byte(svg), 0o644); err != nil {
		return FlamegraphReport{}, fmt.Errorf("flamegraph: write %s: %w", outPath, err)
	}
	return FlamegraphReport{SVG: outPath, StackCount: len(folded), Stacks: folded}, nil
}

// HostSampleCapture captures pid's sample(1) report for durationSec, without
// caching it.
func HostSampleCapture(ctx context.Context, pid, durationSec int) (string, error) {
	res, err := process.NewSampler(nil, nil).Capture(ctx, process.SampleOptions{
		PID:      pid,
		Duration: time.Duration(durationSec) * time.Second,
	})
	if err != nil {
		return "", err
	}
	return res.Output, nil
}
