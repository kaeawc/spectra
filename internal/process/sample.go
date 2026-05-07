package process

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"time"
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
	}
}

// Capture runs `sample <pid> <duration-seconds> <interval-ms>`.
func (s *Sampler) Capture(ctx context.Context, opts SampleOptions) (SampleResult, error) {
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
	out, err := s.runner.Run(runCtx, "sample",
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
