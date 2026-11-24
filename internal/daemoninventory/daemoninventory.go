// Package daemoninventory serves the toolchain inventory, the incident
// timeline and stored-snapshot reads from the local daemon, so clients skip a
// CLI spawn per read. Each method returns exactly the JSON the matching
// `spectra <cmd> --json` prints, built by the same function the CLI calls.
// Nothing is cached: each call collects under its own deadline and stops when
// the client goes away.
package daemoninventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/snapshotquery"
	"github.com/kaeawc/spectra/internal/timeline"
	"github.com/kaeawc/spectra/internal/toolchain"
)

const (
	// MethodToolchain serves `spectra toolchain --json`.
	MethodToolchain = "toolchain.current"
	// MethodTimeline serves `spectra timeline --since <d> --json`.
	MethodTimeline = "timeline.report"
	// MethodSnapshotList serves `spectra snapshot list --json`.
	MethodSnapshotList = "snapshot.list"
	// MethodSnapshotDiff serves `spectra snapshot diff --json`.
	MethodSnapshotDiff = "snapshot.diff"

	DefaultToolchainTimeout    = 60 * time.Second
	DefaultTimelineTimeout     = 2 * time.Minute
	DefaultSnapshotListTimeout = 10 * time.Second
	// DefaultSnapshotDiffTimeout leaves room for a live snapshot, which
	// inspects every installed app on macOS.
	DefaultSnapshotDiffTimeout = 3 * time.Minute

	// DefaultTimelineSince matches the CLI's --since default.
	DefaultTimelineSince = time.Hour
)

// ErrNoStore reports a daemon started without a snapshot store.
var ErrNoStore = errors.New("snapshot store unavailable")

// Options configure a Service. Zero values select production defaults.
type Options struct {
	// Toolchain returns the collector options for one call; the default
	// runs commands that are killed when ctx ends.
	Toolchain func(ctx context.Context) toolchain.CollectOptions
	// TimelineSources returns the timeline sources; the default is the
	// host's, with a ctx-killable process collector.
	TimelineSources func() []timeline.Source
	// Snapshots is the snapshot store; nil makes the snapshot methods fail
	// with ErrNoStore.
	Snapshots snapshotquery.Reader
	// Live captures the snapshot a diff compares as "live". Defaults to
	// DefaultLive("").
	Live   func(ctx context.Context) *snapshot.Snapshot
	Clock  clock.Clock
	Logger logger.Logger

	ToolchainTimeout, TimelineTimeout, SnapshotListTimeout, SnapshotDiffTimeout time.Duration
}

// Service serves the methods.
type Service struct {
	opts Options
}

// NewService fills defaults into opts.
func NewService(opts Options) *Service {
	if opts.Toolchain == nil {
		opts.Toolchain = DefaultToolchain
	}
	if opts.TimelineSources == nil {
		opts.TimelineSources = DefaultTimelineSources
	}
	if opts.Live == nil {
		opts.Live = DefaultLive("")
	}
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Logger == nil {
		opts.Logger = logger.Discard()
	}
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&opts.ToolchainTimeout, DefaultToolchainTimeout)
	def(&opts.TimelineTimeout, DefaultTimelineTimeout)
	def(&opts.SnapshotListTimeout, DefaultSnapshotListTimeout)
	def(&opts.SnapshotDiffTimeout, DefaultSnapshotDiffTimeout)
	return &Service{opts: opts}
}

// DefaultToolchain collects like `spectra toolchain` with commands that are
// killed when ctx ends.
func DefaultToolchain(ctx context.Context) toolchain.CollectOptions {
	return toolchain.CollectOptions{CmdRunner: proc.BoundedRunner(ctx)}
}

// DefaultTimelineSources are the CLI's sources with a ctx-killable ps.
func DefaultTimelineSources() []timeline.Source {
	sources, _ := timeline.HostSources(hostos.Current(), process.CollectOptions{Runner: proc.Default})
	return sources
}

// DefaultLive captures the same snapshot `spectra snapshot diff ... live`
// does, with children that are killed when ctx ends.
func DefaultLive(version string) func(ctx context.Context) *snapshot.Snapshot {
	return func(ctx context.Context) *snapshot.Snapshot {
		run := proc.BoundedRunner(ctx)
		jvmOpts := jvm.CollectOptions{CmdRunner: jvm.ContextRunner(ctx), CmdRunnerContext: jvm.DefaultCtxRunner}
		snap := snapshot.Build(ctx, snapshot.Options{
			SpectraVersion:   version,
			ToolchainOpts:    toolchain.CollectOptions{CmdRunner: run},
			ProcessOpts:      process.CollectOptions{Runner: proc.Default},
			SysinfoCmdRunner: run,
			NetCmdRunner:     netstate.ContextRunner(ctx),
			JVMOpts:          jvmOpts,
			JVMTelemetryOpts: jvm.TelemetryOptions{CollectOptions: jvmOpts},
		})
		return &snap
	}
}

// RegisterMethods registers every method on server.
func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodToolchain, svc.toolchainRPC)
	server.Register(MethodTimeline, svc.timelineRPC)
	server.Register(MethodSnapshotList, svc.snapshotListRPC)
	server.Register(MethodSnapshotDiff, svc.snapshotDiffRPC)
}

// Toolchain returns the `spectra toolchain --json` value.
func (s *Service) Toolchain(ctx context.Context) (toolchain.Toolchains, error) {
	return toolchain.Collect(ctx, s.opts.Toolchain(ctx)), nil
}

// TimelineParams carry `spectra timeline`'s flags. SinceMS is the window in
// milliseconds; zero selects the CLI's one hour default.
type TimelineParams struct {
	SinceMS int64 `json:"since_ms"`
}

// Timeline returns the `spectra timeline --since <since> --json` value.
// Per-source errors, which the CLI prints on stderr, go to the daemon log.
func (s *Service) Timeline(ctx context.Context, since time.Duration) (timeline.Timeline, error) {
	tl, errs := timeline.Report(ctx, since, s.opts.Clock.Now(), s.opts.TimelineSources()...)
	for _, e := range errs {
		s.opts.Logger.Warn("timeline source failed", "error", e)
	}
	return tl, nil
}

// SnapshotListParams carry `spectra snapshot list`'s flags.
type SnapshotListParams struct {
	Kind string `json:"kind,omitempty"`
}

// SnapshotDiffParams carry `spectra snapshot diff`'s operands: A and B (IDs
// or "live"); Baseline with A naming a baseline (empty for the newest) and
// B defaulting to "live"; or SinceMS alone.
type SnapshotDiffParams struct {
	A        string `json:"a,omitempty"`
	B        string `json:"b,omitempty"`
	Baseline bool   `json:"baseline,omitempty"`
	SinceMS  int64  `json:"since_ms,omitempty"`
}

// Options decodes p.
func (p SnapshotDiffParams) Options() snapshotquery.DiffOptions {
	return snapshotquery.DiffOptions{A: p.A, B: p.B, Baseline: p.Baseline, Since: time.Duration(p.SinceMS) * time.Millisecond}
}

func (s *Service) toolchainRPC(ctx context.Context, req *daemon.Request) (any, error) {
	if err := decodeParams(req, &struct{}{}); err != nil {
		return nil, err
	}
	return call(ctx, req, MethodToolchain, s.opts.ToolchainTimeout, func(ctx context.Context) (any, error) {
		return s.Toolchain(ctx)
	})
}

func (s *Service) timelineRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p TimelineParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.SinceMS < 0 {
		return nil, daemon.InvalidParams("since_ms must be positive")
	}
	since := time.Duration(p.SinceMS) * time.Millisecond
	if since == 0 {
		since = DefaultTimelineSince
	}
	return call(ctx, req, MethodTimeline, s.opts.TimelineTimeout, func(ctx context.Context) (any, error) {
		return s.Timeline(ctx, since)
	})
}

func (s *Service) snapshotListRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p SnapshotListParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if !snapshotquery.ValidKind(p.Kind) {
		return nil, daemon.InvalidParams("kind must be live or baseline")
	}
	if s.opts.Snapshots == nil {
		return nil, ErrNoStore
	}
	return call(ctx, req, MethodSnapshotList, s.opts.SnapshotListTimeout, func(ctx context.Context) (any, error) {
		return snapshotquery.List(ctx, s.opts.Snapshots, p.Kind)
	})
}

func (s *Service) snapshotDiffRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p SnapshotDiffParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	opts := p.Options()
	if err := opts.Validate(); err != nil {
		return nil, daemon.InvalidParams("%v", err)
	}
	if s.opts.Snapshots == nil {
		return nil, ErrNoStore
	}
	deps := snapshotquery.DiffDeps{Live: s.opts.Live, Now: s.opts.Clock.Now}
	return call(ctx, req, MethodSnapshotDiff, s.opts.SnapshotDiffTimeout, func(ctx context.Context) (any, error) {
		return snapshotquery.Diff(ctx, s.opts.Snapshots, opts, deps)
	})
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

type result struct {
	value any
	err   error
}

// call runs fn under timeout, cancelling it when the client goes away. fn
// runs on its own goroutine so one that ignores ctx still releases the
// caller at the deadline, and a panic becomes an error. A value produced
// after ctx ended may be partial, so it is discarded.
func call(parent context.Context, req *daemon.Request, name string, timeout time.Duration, fn func(context.Context) (any, error)) (any, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if req.Done != nil {
		go func() {
			select {
			case <-req.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%s: panicked: %v", name, r)}
			}
		}()
		v, err := fn(ctx)
		ch <- result{value: v, err: err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil {
			return nil, fmt.Errorf("%s: timed out after %s: %w", name, timeout, err)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return r.value, r.err
}
