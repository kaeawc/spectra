package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/hostos"
)

// CommandRunner runs a command and returns its stdout.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// CommandRunnerFunc adapts a function into a CommandRunner.
type CommandRunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run calls f(ctx, name, args...).
func (f CommandRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// ExecRunner runs commands through os/exec.
type ExecRunner struct{}

// Run executes name with args and returns stdout.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// SampleStore stores raw sample output.
type SampleStore interface {
	PutSample(ctx context.Context, sample SampleResult) error
}

// SampleOptions controls a bounded macOS sample capture.
type SampleOptions struct {
	PID        int
	Duration   time.Duration
	IntervalMS int
	Store      bool
	TakenAt    time.Time
}

// SampleResult is the output and metadata from one sample capture.
type SampleResult struct {
	PID        int       `json:"pid"`
	TakenAt    time.Time `json:"taken_at"`
	DurationMS int64     `json:"duration_ms"`
	IntervalMS int       `json:"interval_ms"`
	Output     string    `json:"output"`
}

// Sampler captures call stacks with macOS sample(1).
type Sampler struct {
	runner CommandRunner
	store  SampleStore
	now    func() time.Time
	files  ReportFiles
}

// NewSampler constructs a process sampler. Nil runner uses ExecRunner.
func NewSampler(runner CommandRunner, store SampleStore) *Sampler {
	if runner == nil {
		runner = ExecRunner{}
	}
	return &Sampler{
		runner: runner,
		store:  store,
		now:    func() time.Time { return time.Now().UTC() },
		files:  OSReportFiles{},
	}
}

// Capture runs `sample <pid> <duration-seconds> <interval-ms>`.
func (s *Sampler) Capture(ctx context.Context, opts SampleOptions) (SampleResult, error) {
	// sample(1) is a macOS developer tool with no cross-platform
	// equivalent (Linux profiling uses perf); refuse cleanly elsewhere.
	if hostos.Current() != hostos.Darwin {
		return SampleResult{}, hostos.Unsupported("process sampling (sample(1))")
	}
	if opts.PID <= 0 {
		return SampleResult{}, fmt.Errorf("sample requires positive pid")
	}
	if opts.Duration <= 0 {
		opts.Duration = time.Second
	}
	if opts.IntervalMS <= 0 {
		opts.IntervalMS = 10
	}
	if opts.TakenAt.IsZero() {
		opts.TakenAt = s.now()
	}
	timeout := opts.Duration + 5*time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	durationSeconds := int(opts.Duration.Round(time.Second) / time.Second)
	if durationSeconds <= 0 {
		durationSeconds = 1
	}
	out, _, err := RunSampleReport(runCtx, s.files, s.runner.Run,
		strconv.Itoa(opts.PID),
		strconv.Itoa(durationSeconds),
		strconv.Itoa(opts.IntervalMS),
	)
	if err != nil {
		return SampleResult{}, fmt.Errorf("sample pid %d: %w", opts.PID, err)
	}
	result := SampleResult{
		PID:        opts.PID,
		TakenAt:    opts.TakenAt,
		DurationMS: opts.Duration.Milliseconds(),
		IntervalMS: opts.IntervalMS,
		Output:     string(out),
	}
	if opts.Store && s.store != nil {
		if err := s.store.PutSample(ctx, result); err != nil {
			return SampleResult{}, fmt.Errorf("store sample: %w", err)
		}
	}
	return result, nil
}

// ReportFiles creates, reads, and removes the file sample(1) writes its
// report to.
type ReportFiles interface {
	// CreateTemp creates an empty file matching pattern and returns its path.
	CreateTemp(pattern string) (string, error)
	ReadFile(path string) ([]byte, error)
	Remove(path string) error
}

// OSReportFiles is the ReportFiles backed by the real filesystem. Dir ""
// means os.TempDir().
type OSReportFiles struct{ Dir string }

// CreateTemp creates an empty file in Dir and returns its path.
func (o OSReportFiles) CreateTemp(pattern string) (string, error) {
	f, err := os.CreateTemp(o.Dir, pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// ReadFile reads path.
func (OSReportFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// Remove removes path.
func (OSReportFiles) Remove(path string) error { return os.Remove(path) }

// RunSampleReport runs `sample <args...> -file <temp>` through run and
// returns the report read back from that file, plus whatever sample printed
// to stdout (its progress lines, useful in error messages). The temp file is
// always removed. Without -file, sample also writes the report to
// /tmp/<name>_<date>_<rand>.sample.txt and nothing ever cleans it up.
// Nil files uses OSReportFiles{}.
func RunSampleReport(ctx context.Context, files ReportFiles, run func(ctx context.Context, name string, args ...string) ([]byte, error), args ...string) (report, stdout []byte, err error) {
	if files == nil {
		files = OSReportFiles{}
	}
	path, err := files.CreateTemp("spectra-*.sample.txt")
	if err != nil {
		return nil, nil, fmt.Errorf("sample report file: %w", err)
	}
	defer func() { _ = files.Remove(path) }()
	full := append(append([]string(nil), args...), "-file", path)
	stdout, err = run(ctx, "sample", full...)
	if err != nil {
		return nil, stdout, err
	}
	report, err = files.ReadFile(path)
	if err != nil {
		return nil, stdout, fmt.Errorf("read sample report: %w", err)
	}
	return report, stdout, nil
}
