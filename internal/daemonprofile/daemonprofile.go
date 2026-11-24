// Package daemonprofile runs native profiling captures (`spectra sample`,
// `spindump` and `flamegraph`) from the local daemon as background jobs.
// A capture takes seconds, so profile.start returns a job at once and the
// client polls profile.status, then reads profile.result: exactly the JSON
// the matching `spectra <kind> --json` prints, built by the same function.
// Jobs are bounded: few run at once, each has a deadline, and a finished
// job's result is kept only for a TTL.
package daemonprofile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/nativeprofile"
	"github.com/kaeawc/spectra/internal/process"
)

const (
	// MethodStart starts a capture job and returns its record.
	MethodStart = "profile.start"
	// MethodStatus returns a job's record.
	MethodStatus = "profile.status"
	// MethodResult returns a completed job's result.
	MethodResult = "profile.result"
	// MethodCancel cancels a job, killing its capture.
	MethodCancel = "profile.cancel"
	// MethodList returns every job still held.
	MethodList = "profile.list"

	KindSample     = "sample"
	KindSpindump   = "spindump"
	KindFlamegraph = "flamegraph"

	// Job statuses.
	StatusWorking   = "working"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"

	DefaultMaxRunning     = 2
	DefaultMaxFinished    = 16
	DefaultResultTTL      = 10 * time.Minute
	DefaultMaxResultBytes = 8 << 20
	// DefaultSlack is added to a capture's duration for its job deadline,
	// covering tool start-up and symbolication.
	DefaultSlack = 30 * time.Second
	// DefaultPollInterval is the poll_interval_ms a record suggests.
	DefaultPollInterval = time.Second
	// MaxDuration caps duration_ms, as spectra-mcp caps captures.
	MaxDuration = 60 * time.Second
	// MaxIntervalMS caps sample's interval_ms.
	MaxIntervalMS = 1000
)

// Options configure a Service. Zero values select production defaults.
type Options struct {
	// SampleCapture runs sample(1) for KindSample; the default does not
	// cache the report (the daemon only reads).
	SampleCapture nativeprofile.SampleCapture
	// FlamegraphCapture captures the sample report KindFlamegraph folds.
	FlamegraphCapture func(ctx context.Context, pid, durationSec int) (string, error)
	// SpindumpRun runs spindump for KindSpindump.
	SpindumpRun nativeprofile.Runner
	ReadFile    func(string) ([]byte, error)
	WriteFile   func(string, []byte, os.FileMode) error
	// OS selects the platform the captures are gated on; the zero value is
	// the host.
	OS  hostos.Kind
	Now func() time.Time

	MaxRunning, MaxFinished, MaxResultBytes int
	ResultTTL, Slack, PollInterval          time.Duration
}

// Service runs and tracks the jobs.
type Service struct {
	opts Options
	base context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	mu     sync.Mutex
	jobs   map[string]*job
	active int // goroutines still running a capture, cancelled or not
	closed bool
}

// NewService fills defaults into opts.
func NewService(opts Options) *Service {
	if opts.SampleCapture == nil {
		opts.SampleCapture = process.NewSampler(nil, nil).Capture
	}
	if opts.FlamegraphCapture == nil {
		opts.FlamegraphCapture = nativeprofile.HostSampleCapture
	}
	if opts.SpindumpRun == nil {
		opts.SpindumpRun = nativeprofile.HostRunner
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	if opts.WriteFile == nil {
		opts.WriteFile = os.WriteFile
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	defInt := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	defDur := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	defInt(&opts.MaxRunning, DefaultMaxRunning)
	defInt(&opts.MaxFinished, DefaultMaxFinished)
	defInt(&opts.MaxResultBytes, DefaultMaxResultBytes)
	defDur(&opts.ResultTTL, DefaultResultTTL)
	defDur(&opts.Slack, DefaultSlack)
	defDur(&opts.PollInterval, DefaultPollInterval)
	base, stop := context.WithCancel(context.Background())
	return &Service{opts: opts, base: base, stop: stop, jobs: map[string]*job{}}
}

// RegisterMethods registers every method on server.
func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodStart, svc.startRPC)
	server.Register(MethodStatus, svc.statusRPC)
	server.Register(MethodResult, svc.resultRPC)
	server.Register(MethodCancel, svc.cancelRPC)
	server.Register(MethodList, svc.listRPC)
}

// Close cancels every job and waits up to wait for their captures to end.
func (s *Service) Close(wait time.Duration) {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(wait):
	}
}

// StartParams select a capture. Kind is sample, spindump or flamegraph.
//
//   - sample: pid, duration_ms (default 1000), interval_ms (default 10).
//   - spindump: pid and duration_ms (default 2000), or input, an absolute
//     spindump report to parse.
//   - flamegraph: pid and duration_ms (default 2000), or input, an absolute
//     sample report to fold; out, the absolute path the SVG is written to,
//     is required.
//
// duration_ms is whole seconds, at most 60000, as the CLI's --duration.
type StartParams struct {
	Kind       string `json:"kind"`
	PID        int    `json:"pid,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	IntervalMS int    `json:"interval_ms,omitempty"`
	Input      string `json:"input,omitempty"`
	Out        string `json:"out,omitempty"`
}

// IDParams name a job.
type IDParams struct {
	ID string `json:"id"`
}

// Job is a job's public record.
type Job struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	PID    int    `json:"pid,omitempty"`
	Input  string `json:"input,omitempty"`
	Status string `json:"status"`
	// Error is a failed job's error, the text the CLI prints on stderr.
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	DeadlineAt time.Time `json:"deadline_at"`
	// ExpiresAt is when a finished job is dropped.
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	PollIntervalMS int64      `json:"poll_interval_ms"`
	// ResultAvailable is true once profile.result returns the result.
	ResultAvailable bool `json:"result_available"`
}

// JobList is profile.list's result, newest first.
type JobList struct {
	Jobs []Job `json:"jobs"`
}

type job struct {
	rec    Job
	cancel context.CancelFunc
	result json.RawMessage
}

func (s *Service) startRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p StartParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	run, duration, err := s.plan(p)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.start(p, duration, run)
}

// runFunc builds a job's result.
type runFunc func(ctx context.Context) (any, error)

// plan validates p, gates it on macOS, and returns the work and its capture
// duration.
func (s *Service) plan(p StartParams) (runFunc, time.Duration, error) {
	var (
		run runFunc
		d   time.Duration
		err error
	)
	switch p.Kind {
	case KindSample:
		run, d, err = s.planSample(p)
	case KindSpindump:
		run, d, err = s.planSpindump(p)
	case KindFlamegraph:
		run, d, err = s.planFlamegraph(p)
	case "":
		return nil, 0, daemon.InvalidParams("kind is required (sample, spindump or flamegraph)")
	default:
		return nil, 0, daemon.InvalidParams("unknown kind %q (want sample, spindump or flamegraph)", p.Kind)
	}
	if err != nil {
		return nil, 0, err
	}
	if k := hostos.Resolve(s.opts.OS); k != hostos.Darwin {
		return nil, 0, unsupported(p.Kind, k)
	}
	return run, d, nil
}

func (s *Service) planSample(p StartParams) (runFunc, time.Duration, error) {
	if p.Input != "" || p.Out != "" {
		return nil, 0, daemon.InvalidParams("sample takes no input or out")
	}
	if p.PID <= 0 {
		return nil, 0, daemon.InvalidParams("sample needs a positive pid")
	}
	d, err := duration(p.DurationMS, nativeprofile.DefaultSampleDuration)
	if err != nil {
		return nil, 0, err
	}
	interval := p.IntervalMS
	switch {
	case interval < 0 || interval > MaxIntervalMS:
		return nil, 0, daemon.InvalidParams("interval_ms must be between 1 and %d", MaxIntervalMS)
	case interval == 0:
		interval = nativeprofile.DefaultSampleIntervalMS
	}
	opts := process.SampleOptions{PID: p.PID, Duration: d, IntervalMS: interval}
	return func(ctx context.Context) (any, error) {
		_, rep, err := nativeprofile.Sample(ctx, s.opts.SampleCapture, opts)
		return rep, err
	}, d, nil
}

func (s *Service) planSpindump(p StartParams) (runFunc, time.Duration, error) {
	if p.IntervalMS != 0 || p.Out != "" {
		return nil, 0, daemon.InvalidParams("spindump takes no interval_ms or out")
	}
	d, err := s.captureOrInput(p, nativeprofile.DefaultSpindumpDuration)
	if err != nil {
		return nil, 0, err
	}
	opts := nativeprofile.SpindumpOptions{Input: p.Input, ReadFile: s.opts.ReadFile, PID: p.PID, DurationSec: int(d / time.Second), Run: s.opts.SpindumpRun}
	return func(ctx context.Context) (any, error) {
		_, rep, err := nativeprofile.Spindump(ctx, opts)
		return rep, err
	}, d, nil
}

func (s *Service) planFlamegraph(p StartParams) (runFunc, time.Duration, error) {
	if p.IntervalMS != 0 {
		return nil, 0, daemon.InvalidParams("flamegraph takes no interval_ms")
	}
	if p.Out == "" {
		return nil, 0, daemon.InvalidParams("flamegraph needs out, the absolute path to write the SVG to")
	}
	if !filepath.IsAbs(p.Out) {
		return nil, 0, daemon.InvalidParams("out %q must be absolute", p.Out)
	}
	d, err := s.captureOrInput(p, nativeprofile.DefaultFlamegraphDuration)
	if err != nil {
		return nil, 0, err
	}
	opts := nativeprofile.FlamegraphOptions{
		Input: p.Input, ReadFile: s.opts.ReadFile,
		PID: p.PID, DurationSec: int(d / time.Second), Capture: s.opts.FlamegraphCapture,
		Out: p.Out, WriteFile: s.opts.WriteFile,
	}
	return func(ctx context.Context) (any, error) {
		return nativeprofile.Flamegraph(ctx, opts)
	}, d, nil
}

// captureOrInput validates a pid capture or an input report, returning the
// capture duration (zero for input).
func (s *Service) captureOrInput(p StartParams, def time.Duration) (time.Duration, error) {
	if p.Input == "" {
		if p.PID <= 0 {
			return 0, daemon.InvalidParams("%s needs a positive pid or an input report", p.Kind)
		}
		return duration(p.DurationMS, def)
	}
	switch {
	case p.PID != 0:
		return 0, daemon.InvalidParams("pass either input or pid, not both")
	case p.DurationMS != 0:
		return 0, daemon.InvalidParams("duration_ms has no effect with input")
	case !filepath.IsAbs(p.Input):
		return 0, daemon.InvalidParams("input %q must be absolute", p.Input)
	}
	return 0, nil
}

// duration is ms as whole seconds within MaxDuration, or def when zero.
func duration(ms int64, def time.Duration) (time.Duration, error) {
	if ms == 0 {
		return def, nil
	}
	d := time.Duration(ms) * time.Millisecond
	if ms < 0 || d > MaxDuration || d%time.Second != 0 {
		return 0, daemon.InvalidParams("duration_ms must be whole seconds between 1000 and %d", MaxDuration.Milliseconds())
	}
	return d, nil
}

func (s *Service) start(p StartParams, capture time.Duration, run runFunc) (Job, error) {
	id, err := newID()
	if err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Job{}, errors.New("profile: daemon is shutting down")
	}
	s.prune()
	if s.active >= s.opts.MaxRunning {
		return Job{}, fmt.Errorf("profile: %d jobs already running (the limit); wait for one or cancel it with %s", s.active, MethodCancel)
	}
	now := s.opts.Now().UTC()
	deadline := now.Add(capture + s.opts.Slack)
	ctx, cancel := context.WithDeadline(s.base, deadline)
	j := &job{cancel: cancel, rec: Job{
		ID: id, Kind: p.Kind, PID: p.PID, Input: p.Input, Status: StatusWorking,
		CreatedAt: now, UpdatedAt: now, DeadlineAt: deadline,
		PollIntervalMS: s.opts.PollInterval.Milliseconds(),
	}}
	s.jobs[id] = j
	s.active++
	s.wg.Add(1)
	go s.run(ctx, j, capture+s.opts.Slack, run)
	return j.rec, nil
}

// run executes a job and records how it ended; a panic fails the job.
func (s *Service) run(ctx context.Context, j *job, limit time.Duration, run runFunc) {
	defer s.wg.Done()
	defer j.cancel()
	var (
		v   any
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("profile %s: panicked: %v", j.rec.Kind, r)
			}
		}()
		v, err = run(ctx)
	}()
	var raw json.RawMessage
	if err == nil {
		raw, err = json.Marshal(v)
	}
	if err == nil && len(raw) > s.opts.MaxResultBytes {
		err = fmt.Errorf("profile %s: result is %d bytes, over the %d-byte limit", j.rec.Kind, len(raw), s.opts.MaxResultBytes)
	}
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		err = fmt.Errorf("profile %s: timed out after %s: %w", j.rec.Kind, limit, ctxErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active--
	if j.rec.Status != StatusWorking {
		return // cancelled meanwhile
	}
	switch {
	case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
		s.finish(j, StatusCancelled, "")
	case err != nil:
		s.finish(j, StatusFailed, err.Error())
	default:
		j.result = raw
		s.finish(j, StatusCompleted, "")
	}
}

// finish marks j done; the caller holds mu.
func (s *Service) finish(j *job, status, msg string) {
	now := s.opts.Now().UTC()
	exp := now.Add(s.opts.ResultTTL)
	j.rec.Status, j.rec.Error, j.rec.UpdatedAt, j.rec.ExpiresAt = status, msg, now, &exp
	j.rec.ResultAvailable = status == StatusCompleted
}

// prune drops expired finished jobs and, past MaxFinished, the oldest
// finished ones; the caller holds mu.
func (s *Service) prune() {
	now := s.opts.Now().UTC()
	var finished []*job
	for id, j := range s.jobs {
		if j.rec.ExpiresAt == nil {
			continue
		}
		if !now.Before(*j.rec.ExpiresAt) {
			delete(s.jobs, id)
			continue
		}
		finished = append(finished, j)
	}
	if len(finished) <= s.opts.MaxFinished {
		return
	}
	sort.Slice(finished, func(a, b int) bool { return finished[a].rec.UpdatedAt.Before(finished[b].rec.UpdatedAt) })
	for _, j := range finished[:len(finished)-s.opts.MaxFinished] {
		delete(s.jobs, j.rec.ID)
	}
}

// lookup returns the job named by req's params; the caller holds mu.
func (s *Service) lookup(req *daemon.Request) (*job, error) {
	var p IDParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, daemon.InvalidParams("id is required")
	}
	s.prune()
	j, ok := s.jobs[p.ID]
	if !ok {
		return nil, daemon.InvalidParams("unknown profile job %q (finished jobs expire after %s)", p.ID, s.opts.ResultTTL)
	}
	return j, nil
}

func (s *Service) statusRPC(_ context.Context, req *daemon.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.lookup(req)
	if err != nil {
		return nil, err
	}
	return j.rec, nil
}

func (s *Service) resultRPC(_ context.Context, req *daemon.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.lookup(req)
	if err != nil {
		return nil, err
	}
	switch j.rec.Status {
	case StatusCompleted:
		return j.result, nil
	case StatusWorking:
		return nil, jobError{fmt.Sprintf("profile job %s is still working; poll %s", j.rec.ID, MethodStatus), j.rec}
	case StatusCancelled:
		return nil, jobError{fmt.Sprintf("profile job %s was cancelled", j.rec.ID), j.rec}
	default:
		return nil, jobError{j.rec.Error, j.rec}
	}
}

func (s *Service) cancelRPC(_ context.Context, req *daemon.Request) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.lookup(req)
	if err != nil {
		return nil, err
	}
	if j.rec.Status == StatusWorking {
		j.cancel()
		s.finish(j, StatusCancelled, "")
	}
	return j.rec, nil
}

func (s *Service) listRPC(_ context.Context, req *daemon.Request) (any, error) {
	if err := decodeParams(req, &struct{}{}); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	out := JobList{Jobs: make([]Job, 0, len(s.jobs))}
	for _, j := range s.jobs {
		out.Jobs = append(out.Jobs, j.rec)
	}
	sort.Slice(out.Jobs, func(a, b int) bool {
		if !out.Jobs[a].CreatedAt.Equal(out.Jobs[b].CreatedAt) {
			return out.Jobs[a].CreatedAt.After(out.Jobs[b].CreatedAt)
		}
		return out.Jobs[a].ID < out.Jobs[b].ID
	})
	return out, nil
}

// jobError is profile.result's error for a job without a result; its data
// is the job record.
type jobError struct {
	msg string
	rec Job
}

func (e jobError) Error() string  { return e.msg }
func (e jobError) ErrorData() any { return e.rec }

var features = map[string]string{
	KindSample:     nativeprofile.SampleFeature,
	KindSpindump:   nativeprofile.SpindumpFeature,
	KindFlamegraph: nativeprofile.FlamegraphFeature,
}

// unsupported is profile.start's error off macOS: the CLI's message, with
// the `spectra <kind> --json` unsupported object as data.
func unsupported(kind string, k hostos.Kind) error {
	err := &hostos.UnsupportedError{Feature: features[kind], OS: k}
	r, _ := hostos.ReportCommandUnsupported(kind, err, nativeprofile.OffDarwinHint)
	return unsupportedError{err: err, report: r}
}

type unsupportedError struct {
	err    error
	report hostos.CommandReport
}

func (e unsupportedError) Error() string  { return e.err.Error() }
func (e unsupportedError) Unwrap() error  { return e.err }
func (e unsupportedError) ErrorData() any { return e.report }

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("profile job id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// decodeParams accepts absent or null params as the zero value.
func decodeParams(req *daemon.Request, dst any) error {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		return daemon.InvalidParams("invalid params: %v", err)
	}
	return nil
}
