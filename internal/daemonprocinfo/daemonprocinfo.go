// Package daemonprocinfo serves per-process and per-service reads (process
// lineage, owner lookups, launchd services and unified-log queries) from the
// local daemon, so clients skip a CLI spawn per read. Each method returns
// exactly the JSON the matching `spectra <cmd> --json` prints, built by the
// same function the CLI calls. Nothing is cached: each call collects under
// its own deadline and stops when the client goes away.
package daemonprocinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/lineage"
	"github.com/kaeawc/spectra/internal/logquery"
	"github.com/kaeawc/spectra/internal/owner"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/services"
)

const (
	// MethodLineage serves `spectra lineage --json <pid>`.
	MethodLineage = "lineage.report"
	// MethodOwner serves `spectra owner --json <target>` and
	// `spectra owner --under --json <dir>...`.
	MethodOwner = "owner.report"
	// MethodServices serves `spectra services --json`.
	MethodServices = "services.list"
	// MethodLogs serves `spectra logs --json` (not --stream).
	MethodLogs = "logs.query"

	DefaultLineageTimeout  = 30 * time.Second
	DefaultServicesTimeout = 30 * time.Second
	DefaultLogsTimeout     = 2 * time.Minute
	// MaxOwnerTimeout caps owner.report's timeout_ms; the default is the
	// CLI's 30 second --timeout.
	MaxOwnerTimeout = 5 * time.Minute

	// The CLI's `spectra logs` flag defaults.
	DefaultLogsLast = time.Hour
	DefaultLogsTop  = 5000
)

// Options configure a Service. Zero values select production defaults.
type Options struct {
	// Processes and ProcessOpts collect the table lineage walks; a nil
	// ProcessOpts.Runner is the ctx-killable proc.Default.
	Processes   process.Collector
	ProcessOpts process.CollectOptions
	// OwnerFind and OwnerUnder answer owner.report. OwnerUnder gets the
	// caller's exclude_pid. Defaults are owner.Finder's, whose lsof and ps
	// children are killed when ctx ends.
	OwnerFind  owner.FindFunc
	OwnerUnder func(excludePID int) owner.UnderFunc
	// Services is the base services.List options (runner, plist dirs).
	Services services.Options
	// Logs is the base query (OS, runner, clock) logs.query fills in.
	Logs logquery.Query
	// OS selects the platform the macOS-only methods are gated on; the zero
	// value is the host.
	OS hostos.Kind

	LineageTimeout, ServicesTimeout, LogsTimeout time.Duration
}

// Service serves the methods.
type Service struct {
	opts Options
}

// NewService fills defaults into opts.
func NewService(opts Options) *Service {
	if opts.Processes == nil {
		opts.Processes = process.SystemCollector{}
	}
	if opts.ProcessOpts.Runner == nil {
		opts.ProcessOpts.Runner = proc.Default
	}
	if opts.OwnerFind == nil {
		opts.OwnerFind = owner.Finder{}.Find
	}
	if opts.OwnerUnder == nil {
		opts.OwnerUnder = func(excludePID int) owner.UnderFunc { return owner.Finder{ExcludePID: excludePID}.Under }
	}
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&opts.LineageTimeout, DefaultLineageTimeout)
	def(&opts.ServicesTimeout, DefaultServicesTimeout)
	def(&opts.LogsTimeout, DefaultLogsTimeout)
	return &Service{opts: opts}
}

// RegisterMethods registers every method on server.
func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodLineage, svc.lineageRPC)
	server.Register(MethodOwner, svc.ownerRPC)
	server.Register(MethodServices, svc.servicesRPC)
	server.Register(MethodLogs, svc.logsRPC)
}

// LineageParams carry `spectra lineage`'s pid.
type LineageParams struct {
	PID int `json:"pid"`
}

// OwnerParams carry `spectra owner`'s arguments: Target (":8080",
// "tcp:8080", "udp:53", an absolute path, or a pid), or Under with Dirs
// (absolute directories). TimeoutMS defaults to the CLI's 30 seconds.
// ExcludePID is the caller, which --under leaves out with its children as
// the CLI leaves out itself.
type OwnerParams struct {
	Target     string   `json:"target,omitempty"`
	Under      bool     `json:"under,omitempty"`
	Dirs       []string `json:"dirs,omitempty"`
	TimeoutMS  int64    `json:"timeout_ms,omitempty"`
	ExcludePID int      `json:"exclude_pid,omitempty"`
}

// ServicesParams carry `spectra services`' flags.
type ServicesParams struct {
	Domain   string `json:"domain,omitempty"`
	Label    string `json:"label,omitempty"`
	Running  bool   `json:"running,omitempty"`
	OnDemand bool   `json:"on_demand,omitempty"`
}

// LogsParams carry `spectra logs`' query flags; LastMS and Top default to
// the CLI's one hour and 5000 rows. Raw predicates and --stream are not
// served.
type LogsParams struct {
	Process         string `json:"process,omitempty"`
	Subsystem       string `json:"subsystem,omitempty"`
	Level           string `json:"level,omitempty"`
	LastMS          int64  `json:"last_ms,omitempty"`
	Grep            string `json:"grep,omitempty"`
	Top             int    `json:"top,omitempty"`
	AllowLongWindow bool   `json:"allow_long_window,omitempty"`
}

func (s *Service) lineageRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p LineageParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.PID <= 0 {
		return nil, daemon.InvalidParams("positive pid required")
	}
	return call(ctx, req, MethodLineage, s.opts.LineageTimeout, func(ctx context.Context) (any, error) {
		res, err := lineage.Collect(ctx, s.opts.Processes, s.opts.ProcessOpts, p.PID)
		if err != nil {
			return nil, fmt.Errorf("lineage: %w", err)
		}
		return res, nil
	})
}

func (s *Service) ownerRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p OwnerParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	switch {
	case p.TimeoutMS < 0:
		return nil, daemon.InvalidParams("timeout_ms must be positive")
	case timeout > MaxOwnerTimeout:
		return nil, daemon.InvalidParams("timeout_ms must be at most %d", MaxOwnerTimeout.Milliseconds())
	case timeout == 0:
		timeout = owner.DefaultTimeout
	}
	if p.Under {
		return s.ownerUnder(ctx, req, p, timeout)
	}
	if len(p.Dirs) > 0 || p.ExcludePID != 0 {
		return nil, daemon.InvalidParams("dirs and exclude_pid need under")
	}
	t, err := owner.ParseTarget(p.Target, "/")
	if err != nil {
		return nil, daemon.InvalidParams("%v", err)
	}
	if t.Kind == owner.KindPath && !filepath.IsAbs(strings.TrimSpace(p.Target)) {
		return nil, daemon.InvalidParams("path target %q must be absolute", p.Target)
	}
	return call(ctx, req, MethodOwner, timeout, func(ctx context.Context) (any, error) {
		res, err := owner.BuildReport(ctx, t, s.opts.OwnerFind)
		if err != nil {
			return nil, fmt.Errorf("owner: %w", err)
		}
		return res, nil
	})
}

func (s *Service) ownerUnder(ctx context.Context, req *daemon.Request, p OwnerParams, timeout time.Duration) (any, error) {
	if p.Target != "" {
		return nil, daemon.InvalidParams("target cannot be combined with under")
	}
	if len(p.Dirs) == 0 {
		return nil, daemon.InvalidParams("under needs at least one dir")
	}
	for _, d := range p.Dirs {
		if !filepath.IsAbs(d) {
			return nil, daemon.InvalidParams("dir %q must be absolute", d)
		}
	}
	if p.ExcludePID < 0 {
		return nil, daemon.InvalidParams("exclude_pid must be positive")
	}
	dirs := owner.AbsDirs(p.Dirs, "/")
	under := s.opts.OwnerUnder(p.ExcludePID)
	return call(ctx, req, MethodOwner, timeout, func(ctx context.Context) (any, error) {
		res, err := owner.BuildUnderReport(ctx, dirs, under)
		if err != nil {
			return nil, fmt.Errorf("owner: %w", err)
		}
		return res, nil
	})
}

func (s *Service) servicesRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p ServicesParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if !services.ValidDomain(p.Domain) {
		return nil, daemon.InvalidParams("domain must be system, user, or all")
	}
	if err := s.darwinOnly("services", services.Feature, services.OffDarwinHint); err != nil {
		return nil, err
	}
	opts := s.opts.Services
	opts.Domain = p.Domain
	if opts.Domain == "" {
		opts.Domain = "system"
	}
	f := services.Filter{Label: p.Label, Running: p.Running, OnDemand: p.OnDemand}
	return call(ctx, req, MethodServices, s.opts.ServicesTimeout, func(ctx context.Context) (any, error) {
		return services.Report(ctx, opts, f)
	})
}

func (s *Service) logsRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p LogsParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.LastMS < 0 || p.Top < 0 {
		return nil, daemon.InvalidParams("last_ms and top must be positive")
	}
	var re *regexp.Regexp
	if p.Grep != "" {
		var err error
		if re, err = regexp.Compile(p.Grep); err != nil {
			return nil, daemon.InvalidParams("grep: %v", err)
		}
	}
	if err := s.darwinOnly("logs", logquery.Feature, logquery.OffDarwinHint); err != nil {
		return nil, err
	}
	q := s.opts.Logs
	q.Process, q.Subsystem, q.MinLevel, q.AllowLongWindow = p.Process, p.Subsystem, p.Level, p.AllowLongWindow
	q.Last, q.MaxRows = DefaultLogsLast, DefaultLogsTop
	if p.LastMS > 0 {
		q.Last = time.Duration(p.LastMS) * time.Millisecond
	}
	if p.Top > 0 {
		q.MaxRows = p.Top
	}
	return call(ctx, req, MethodLogs, s.opts.LogsTimeout, func(ctx context.Context) (any, error) {
		res, err := logquery.Report(ctx, q, re)
		if errors.Is(err, logquery.ErrWindowTooLarge) || errors.Is(err, logquery.ErrUnsafePredicate) {
			return nil, daemon.InvalidParams("%v", err)
		}
		if err != nil {
			return nil, err
		}
		return res, nil
	})
}

// darwinOnly answers a macOS-only method off macOS with the error the CLI
// reports, carrying the `spectra <cmd> --json` unsupported object as data.
func (s *Service) darwinOnly(cmd, feature, hint string) error {
	if hostos.Resolve(s.opts.OS) == hostos.Darwin {
		return nil
	}
	err := hostos.Unsupported(feature)
	r, _ := hostos.ReportCommandUnsupported(cmd, err, hint)
	return unsupportedError{err: err, report: r}
}

type unsupportedError struct {
	err    error
	report hostos.CommandReport
}

func (e unsupportedError) Error() string  { return e.err.Error() }
func (e unsupportedError) Unwrap() error  { return e.err }
func (e unsupportedError) ErrorData() any { return e.report }

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
