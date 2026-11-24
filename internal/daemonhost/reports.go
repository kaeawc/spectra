package daemonhost

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/idle"
)

const (
	// MethodLoad serves `spectra load --json` for the given flags.
	MethodLoad = "load.report"
	// MethodIdle serves `spectra idle --json` for the given flags.
	MethodIdle = "idle.report"

	loadReportTimeout = 30 * time.Second
	idleMaxTimeout    = 5 * time.Minute
)

// LoadParams carry `spectra load`'s flags; durations are milliseconds and
// roots must be absolute, since the daemon's working directory is not the
// caller's.
type LoadParams struct {
	Top           int      `json:"top"`
	Roots         []string `json:"roots,omitempty"`
	MaxLoadPerCPU float64  `json:"max_load_per_cpu"`
	MinFreeGB     float64  `json:"min_free_gb"`
	BackoffFreeGB float64  `json:"backoff_free_gb"`
	SampleMS      int64    `json:"sample_ms"`
	BootWindowMS  int64    `json:"boot_window_ms"`
}

// LoadParamsFrom encodes opts for the wire.
func LoadParamsFrom(o hostload.ReportOptions) LoadParams {
	return LoadParams{
		Top: o.Top, Roots: o.Roots, MaxLoadPerCPU: o.MaxLoadPerCPU, MinFreeGB: o.MinFreeGB, BackoffFreeGB: o.BackoffFreeGB,
		SampleMS: o.Sample.Milliseconds(), BootWindowMS: o.BootWindow.Milliseconds(),
	}
}

// Options decodes p.
func (p LoadParams) Options() hostload.ReportOptions {
	return hostload.ReportOptions{
		Top: p.Top, Roots: p.Roots, MaxLoadPerCPU: p.MaxLoadPerCPU, MinFreeGB: p.MinFreeGB, BackoffFreeGB: p.BackoffFreeGB,
		Sample: time.Duration(p.SampleMS) * time.Millisecond, BootWindow: time.Duration(p.BootWindowMS) * time.Millisecond,
	}
}

// IdleParams carry `spectra idle`'s flags. Roots or PIDs are required and
// roots must be absolute. ExcludePID is the caller, whose ancestors and
// descendants are left out as the CLI leaves out its own.
type IdleParams struct {
	Roots      []string `json:"roots,omitempty"`
	PIDs       []int    `json:"pids,omitempty"`
	MinAgeMS   int64    `json:"min_age_ms"`
	MaxCPU     float64  `json:"max_cpu"`
	WindowMS   int64    `json:"window_ms"`
	TimeoutMS  int64    `json:"timeout_ms"`
	ExcludePID int      `json:"exclude_pid"`
}

// IdleParamsFrom encodes opts for the wire.
func IdleParamsFrom(o idle.ReportOptions, excludePID int) IdleParams {
	return IdleParams{
		Roots: o.Roots, PIDs: o.PIDs, MinAgeMS: o.MinAge.Milliseconds(), MaxCPU: o.MaxCPU,
		WindowMS: o.Window.Milliseconds(), TimeoutMS: o.Timeout.Milliseconds(), ExcludePID: excludePID,
	}
}

// Options decodes p.
func (p IdleParams) Options() idle.ReportOptions {
	ms := func(v int64) time.Duration { return time.Duration(v) * time.Millisecond }
	return idle.ReportOptions{Roots: p.Roots, PIDs: p.PIDs, MinAge: ms(p.MinAgeMS), MaxCPU: p.MaxCPU, Window: ms(p.WindowMS), Timeout: ms(p.TimeoutMS)}
}

// Reports serves load and idle with one shared CPU tracker, so a call
// diffs against a reading the daemon already holds instead of sleeping
// through its window.
type Reports struct {
	Load func() hostload.ReportDeps
	Idle func() idle.ReportDeps
}

// NewReports reads the real host.
func NewReports() *Reports {
	tracker := hostload.NewTracker()
	return &Reports{
		Load: func() hostload.ReportDeps {
			d := hostload.DefaultReportDeps()
			d.Baseline = tracker.Baseline
			return d
		},
		Idle: func() idle.ReportDeps { return idle.HostReportDeps(tracker.Baseline) },
	}
}

// RegisterReports registers load.report and idle.report on server.
func RegisterReports(server *daemon.Server, r *Reports) {
	server.Register(MethodLoad, r.rpcLoad)
	server.Register(MethodIdle, r.rpcIdle)
}

func (r *Reports) rpcLoad(ctx context.Context, req *daemon.Request) (any, error) {
	var p LoadParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	opts := p.Options()
	if err := opts.Validate(); err != nil {
		return nil, daemon.InvalidParams("%v", err)
	}
	if err := absolute(opts.Roots); err != nil {
		return nil, err
	}
	ctx, cancel := callContext(ctx, req, loadReportTimeout)
	defer cancel()
	return hostload.BuildReport(ctx, opts, r.Load())
}

func (r *Reports) rpcIdle(ctx context.Context, req *daemon.Request) (any, error) {
	var p IdleParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	opts := p.Options()
	if err := opts.Validate(); err != nil {
		return nil, daemon.InvalidParams("%v", err)
	}
	if opts.Timeout > idleMaxTimeout {
		return nil, daemon.InvalidParams("timeout must be at most %s", idleMaxTimeout)
	}
	if len(opts.Roots) == 0 && len(opts.PIDs) == 0 {
		return nil, daemon.InvalidParams("roots or pids are required")
	}
	if err := absolute(opts.Roots); err != nil {
		return nil, err
	}
	deps := r.Idle()
	if p.ExcludePID > 0 {
		deps.Pid = func() int { return p.ExcludePID }
	}
	ctx, cancel := callContext(ctx, req, opts.Timeout)
	defer cancel()
	return idle.BuildReport(ctx, opts, deps)
}

func decodeParams(req *daemon.Request, out any) error {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return daemon.InvalidParams("params are required")
	}
	if err := json.Unmarshal(req.Params, out); err != nil {
		return daemon.InvalidParams("invalid params: %v", err)
	}
	return nil
}

func absolute(roots []string) error {
	for _, r := range roots {
		if !filepath.IsAbs(r) {
			return daemon.InvalidParams("root %q must be absolute", r)
		}
	}
	return nil
}

// callContext bounds a call by timeout and ends it when the client goes away.
func callContext(ctx context.Context, req *daemon.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	if req.Done != nil {
		go func() {
			select {
			case <-req.Done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
