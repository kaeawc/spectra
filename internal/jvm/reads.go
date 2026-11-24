package jvm

import (
	"context"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/heap"
)

// The per-pid read builders below produce the values `spectra jvm <cmd>
// --json` prints, and the errors carry the CLI's stderr text, so the daemon
// can return both unchanged.

// DefaultHistogramSuspects is how many classes heap-histogram ranks when the
// caller asks for none.
const DefaultHistogramSuspects = 20

// ThreadDumpReport is the `spectra jvm thread-dump --json` value.
type ThreadDumpReport struct {
	Summary ThreadSummary    `json:"summary"`
	Dump    ParsedThreadDump `json:"dump"`
	// CodeSources maps a frame's class name to its jar or class file, for
	// the classes that could be found cheaply.
	CodeSources map[string]CodeSource `json:"code_sources,omitempty"`
}

// CaptureThreadDump runs Thread.print against pid.
func CaptureThreadDump(pid int, run CmdRunner) ([]byte, error) {
	data, err := ThreadDump(pid, run)
	if err != nil {
		return nil, fmt.Errorf("thread-dump failed for PID %d: %w", pid, err)
	}
	return data, nil
}

// NewThreadDumpReport parses a raw Thread.print capture.
func NewThreadDumpReport(raw []byte, capturedAt time.Time) ThreadDumpReport {
	dump := ParseThreadDump(string(raw), capturedAt)
	return ThreadDumpReport{Summary: SummarizeThreads(dump), Dump: dump}
}

// HeapHistogramReport is the `spectra jvm heap-histogram --json` value.
type HeapHistogramReport struct {
	Histogram heap.Histogram `json:"histogram"`
	Suspects  []heap.Suspect `json:"suspects,omitempty"`
}

// ReadHeapHistogram captures GC.class_histogram and ranks the largest
// classes; suspects <= 0 selects DefaultHistogramSuspects.
func ReadHeapHistogram(pid, suspects int, run CmdRunner) (HeapHistogramReport, error) {
	hist, err := HeapHistogramSnapshot(pid, run)
	if err != nil {
		return HeapHistogramReport{}, fmt.Errorf("heap-histogram failed for PID %d: %w", pid, err)
	}
	if suspects <= 0 {
		suspects = DefaultHistogramSuspects
	}
	return HeapHistogramReport{Histogram: hist, Suspects: heap.RankHistogramSuspects(hist, suspects)}, nil
}

// ReadGCStats samples jstat -gc once.
func ReadGCStats(pid int, run CmdRunner) (*GCStats, error) {
	stats, err := CollectGCStats(pid, run)
	if err != nil {
		return nil, fmt.Errorf("gc-stats failed for PID %d: %w", pid, err)
	}
	return stats, nil
}

// ReadExplanation is CollectExplanation with the CLI's error text.
func ReadExplanation(ctx context.Context, pid int, opts ExplainOptions) (*Explanation, error) {
	e, err := CollectExplanation(ctx, pid, opts)
	if err != nil {
		return nil, fmt.Errorf("explain failed for PID %d: %w", pid, err)
	}
	return e, nil
}

// NMT read modes. "baseline" is absent on purpose: it changes the target
// JVM's state rather than reading it.
const (
	NMTModeSummary = "summary"
	NMTModeDetail  = "detail"
	NMTModeDiff    = "diff"
)

// ErrNMTDisabledHint is ErrNMTDisabled with the restart advice the CLI prints.
var ErrNMTDisabledHint = fmt.Errorf("%w; restart the JVM with -XX:NativeMemoryTracking=summary (or =detail for call sites)", ErrNMTDisabled)

// captureNMT runs VM.native_memory <mode>.
func captureNMT(pid int, mode string, run CmdRunner) (string, error) {
	out, err := NativeMemory(pid, mode, run)
	if err != nil {
		return "", fmt.Errorf("nmt %s failed for PID %d: %w", mode, pid, err)
	}
	return string(out), nil
}

// ReadNMTSummary is the `spectra jvm nmt --json` value.
func ReadNMTSummary(pid int, run CmdRunner) (NMTBreakdown, error) {
	out, err := captureNMT(pid, "summary", run)
	if err != nil {
		return NMTBreakdown{}, err
	}
	b := ParseNMTSummary(out)
	if !b.Enabled {
		return NMTBreakdown{}, ErrNMTDisabledHint
	}
	return b, nil
}

// ReadNMTDetail is the `spectra jvm nmt detail --json` value.
func ReadNMTDetail(pid int, run CmdRunner) (NMTDetail, error) {
	out, err := captureNMT(pid, "detail", run)
	if err != nil {
		return NMTDetail{}, err
	}
	d := ParseNMTDetail(out)
	if !d.Summary.Enabled {
		return NMTDetail{}, ErrNMTDisabledHint
	}
	return d, nil
}

// ReadNMTDiff is the `spectra jvm nmt diff --json` value: growth since the
// baseline a previous `nmt baseline` recorded.
func ReadNMTDiff(pid int, run CmdRunner) (NMTDiff, error) {
	out, err := captureNMT(pid, "summary.diff", run)
	if err != nil {
		return NMTDiff{}, err
	}
	d, err := ParseNMTSummaryDiff(out)
	if err != nil {
		return NMTDiff{}, fmt.Errorf("nmt diff: %w (run `spectra jvm nmt baseline <pid>` first)", err)
	}
	return d, nil
}

// ReadNMT dispatches on mode; an unknown mode is an error.
func ReadNMT(pid int, mode string, run CmdRunner) (any, error) {
	switch mode {
	case NMTModeSummary, "":
		return ReadNMTSummary(pid, run)
	case NMTModeDetail:
		return ReadNMTDetail(pid, run)
	case NMTModeDiff:
		return ReadNMTDiff(pid, run)
	}
	return nil, fmt.Errorf("unknown nmt mode %q", mode)
}
