// Package daemoncrash serves crash-report reads and atos symbolication from
// the local daemon, so clients skip a CLI spawn per read. Each method returns
// exactly the JSON the matching `spectra crash list|signatures|oom|inspect
// --json` or `spectra symbolicate --json` prints, built by the same function
// the CLI calls. Nothing is cached: each call reads under its own deadline
// and stops when the client goes away.
package daemoncrash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/kaeawc/spectra/internal/crashquery"
	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/symbolicate"
)

const (
	// MethodList serves `spectra crash list --json`.
	MethodList = "crash.list"
	// MethodSignatures serves `spectra crash signatures --json`.
	MethodSignatures = "crash.signatures"
	// MethodOOM serves `spectra crash oom --json`.
	MethodOOM = "crash.oom"
	// MethodInspect serves `spectra crash inspect --json`.
	MethodInspect = "crash.inspect"
	// MethodSymbolicate serves `spectra symbolicate --json`.
	MethodSymbolicate = "symbolicate.addresses"

	DefaultSweepTimeout       = 2 * time.Minute
	DefaultInspectTimeout     = 3 * time.Minute
	DefaultSymbolicateTimeout = time.Minute

	// MaxAddresses bounds one symbolicate.addresses call, keeping atos's
	// argument list well under the OS limit.
	MaxAddresses = 4096
)

// dirHint tells an RPC caller how to answer a crash sweep off macOS.
const dirHint = "pass dir with an absolute directory of .ips reports"

// archPattern is an atos -arch value, such as arm64, x86_64 or arm64e.
var archPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// Options configure a Service. Zero values select production defaults.
type Options struct {
	// Source opens a reports directory; the default reads the filesystem.
	Source func(dir string) crashquery.Source
	// Read reads one report for crash.inspect; the default is os.ReadFile.
	Read func(string) ([]byte, error)
	// Open opens an image binary for crash.inspect's on-disk check.
	Open crashreport.OpenFunc
	// SourceLines is the base for crash.inspect's source_lines; a nil
	// Runner is the ctx-killable proc.Default.
	SourceLines crashreport.SourceLineOptions
	// Symbolicate runs atos; the default is symbolicate.HostRunner.
	Symbolicate symbolicate.Runner
	// Home is the user's home directory, for the default DiagnosticReports
	// directory; empty resolves os.UserHomeDir.
	Home string
	// OS selects the platform the macOS-only parts are gated on; the zero
	// value is the host.
	OS hostos.Kind

	SweepTimeout, InspectTimeout, SymbolicateTimeout time.Duration
}

// Service serves the methods.
type Service struct {
	opts Options
}

// NewService fills defaults into opts.
func NewService(opts Options) *Service {
	if opts.Source == nil {
		opts.Source = crashquery.DirSource
	}
	if opts.Read == nil {
		opts.Read = os.ReadFile
	}
	if opts.Open == nil {
		opts.Open = crashreport.OpenOS
	}
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.SourceLines.Runner == nil {
		opts.SourceLines.Runner = proc.Default
	}
	if opts.SourceLines.FS == nil {
		opts.SourceLines.FS = crashreport.OSSourceFS()
	}
	if opts.SourceLines.Home == "" {
		opts.SourceLines.Home = opts.Home
	}
	if opts.SourceLines.OS == hostos.Unknown {
		opts.SourceLines.OS = opts.OS
	}
	if opts.Symbolicate == nil {
		opts.Symbolicate = symbolicate.HostRunner
	}
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&opts.SweepTimeout, DefaultSweepTimeout)
	def(&opts.InspectTimeout, DefaultInspectTimeout)
	def(&opts.SymbolicateTimeout, DefaultSymbolicateTimeout)
	return &Service{opts: opts}
}

// RegisterMethods registers every method on server.
func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodList, svc.listRPC)
	server.Register(MethodSignatures, svc.signaturesRPC)
	server.Register(MethodOOM, svc.oomRPC)
	server.Register(MethodInspect, svc.inspectRPC)
	server.Register(MethodSymbolicate, svc.symbolicateRPC)
}

// SweepParams carry `spectra crash list|signatures|oom`'s flags. Dir is an
// absolute directory of .ips reports, defaulting to the user's
// ~/Library/Logs/DiagnosticReports on macOS. Limit defaults to the CLI's 25;
// 0 keeps every row.
type SweepParams struct {
	Dir   string `json:"dir,omitempty"`
	Limit *int   `json:"limit,omitempty"`
}

// InspectParams carry `spectra crash inspect`'s report path and flags. All
// mirrors --all, which only changes the text output: the JSON always holds
// every thread.
type InspectParams struct {
	Path        string `json:"path"`
	All         bool   `json:"all,omitempty"`
	SourceLines bool   `json:"source_lines,omitempty"`
}

// SymbolicateParams carry `spectra symbolicate`'s -o, -l, --arch and
// addresses.
type SymbolicateParams struct {
	Binary      string   `json:"binary"`
	LoadAddress string   `json:"load_address"`
	Arch        string   `json:"arch,omitempty"`
	Addresses   []string `json:"addresses"`
}

func (s *Service) listRPC(ctx context.Context, req *daemon.Request) (any, error) {
	return s.sweepRPC(ctx, req, MethodList, "crash list", func(ctx context.Context, src crashquery.Source, limit int) (any, error) {
		res, err := crashquery.List(ctx, src, limit)
		return res.Summaries, err
	})
}

func (s *Service) signaturesRPC(ctx context.Context, req *daemon.Request) (any, error) {
	return s.sweepRPC(ctx, req, MethodSignatures, "crash signatures", func(ctx context.Context, src crashquery.Source, limit int) (any, error) {
		res, err := crashquery.Signatures(ctx, src, limit)
		return res.Signatures, err
	})
}

func (s *Service) oomRPC(ctx context.Context, req *daemon.Request) (any, error) {
	return s.sweepRPC(ctx, req, MethodOOM, "crash oom", func(ctx context.Context, src crashquery.Source, limit int) (any, error) {
		res, err := crashquery.OOM(ctx, src, limit)
		return res.Kills, err
	})
}

func (s *Service) sweepRPC(ctx context.Context, req *daemon.Request, method, cmd string, build func(context.Context, crashquery.Source, int) (any, error)) (any, error) {
	var p SweepParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	limit := crashquery.DefaultLimit
	if p.Limit != nil {
		if *p.Limit < 0 {
			return nil, daemon.InvalidParams("limit must not be negative")
		}
		limit = *p.Limit
	}
	dir := p.Dir
	if dir != "" && !filepath.IsAbs(dir) {
		return nil, daemon.InvalidParams("dir %q must be absolute", dir)
	}
	if dir == "" {
		var err error
		if dir, err = s.defaultDir(cmd); err != nil {
			return nil, err
		}
	}
	src := s.opts.Source(dir)
	return call(ctx, req, method, s.opts.SweepTimeout, func(ctx context.Context) (any, error) {
		return build(ctx, src, limit)
	})
}

// defaultDir is the DiagnosticReports directory, or the error the CLI gives
// without --dir: unsupported off macOS (with the unsupported object as
// data), or no home directory.
func (s *Service) defaultDir(cmd string) (string, error) {
	k := hostos.Resolve(s.opts.OS)
	if k != hostos.Darwin {
		return "", unsupported(cmd, crashquery.ReportsFeature, k, dirHint)
	}
	dir := crashquery.DefaultDir(k, s.opts.Home)
	if dir == "" {
		return "", fmt.Errorf("%s: cannot resolve the home directory; pass dir", cmd)
	}
	return dir, nil
}

func (s *Service) inspectRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p InspectParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, daemon.InvalidParams("path is required")
	}
	if !filepath.IsAbs(p.Path) {
		return nil, daemon.InvalidParams("path %q must be absolute", p.Path)
	}
	opts := crashquery.InspectOptions{Read: s.opts.Read, Open: s.opts.Open, SourceLines: p.SourceLines, Source: s.opts.SourceLines}
	return call(ctx, req, MethodInspect, s.opts.InspectTimeout, func(ctx context.Context) (any, error) {
		return crashquery.Inspect(ctx, p.Path, opts)
	})
}

func (s *Service) symbolicateRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p SymbolicateParams
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	if err := validateSymbolicate(p); err != nil {
		return nil, err
	}
	if k := hostos.Resolve(s.opts.OS); k != hostos.Darwin {
		return nil, unsupported("symbolicate", symbolicate.Feature, k, "")
	}
	r := symbolicate.Request{Binary: p.Binary, LoadAddress: p.LoadAddress, Arch: p.Arch, Addresses: p.Addresses}
	return call(ctx, req, MethodSymbolicate, s.opts.SymbolicateTimeout, func(ctx context.Context) (any, error) {
		return symbolicate.Symbolicate(ctx, r, s.opts.Symbolicate)
	})
}

// validateSymbolicate rejects what the CLI would reject, plus anything that
// atos could read as a flag rather than a path, arch or address.
func validateSymbolicate(p SymbolicateParams) error {
	switch {
	case p.Binary == "":
		return daemon.InvalidParams("binary is required")
	case !filepath.IsAbs(p.Binary):
		return daemon.InvalidParams("binary %q must be absolute", p.Binary)
	case p.LoadAddress == "":
		return daemon.InvalidParams("load_address is required")
	case !symbolicate.IsHexAddress(p.LoadAddress):
		return daemon.InvalidParams("invalid load_address %q (want a hex value like 0x10a400000)", p.LoadAddress)
	case p.Arch != "" && !archPattern.MatchString(p.Arch):
		return daemon.InvalidParams("invalid arch %q", p.Arch)
	case len(p.Addresses) == 0:
		return daemon.InvalidParams("addresses are required")
	case len(p.Addresses) > MaxAddresses:
		return daemon.InvalidParams("at most %d addresses per call", MaxAddresses)
	}
	for _, a := range p.Addresses {
		if !symbolicate.IsHexAddress(a) {
			return daemon.InvalidParams("invalid address %q (want a hex value like 0x10a4f1234)", a)
		}
	}
	return nil
}

// unsupported is the error a macOS-only part gives off macOS: the CLI's
// message, with the `spectra <cmd> --json` unsupported object as data.
func unsupported(cmd, feature string, k hostos.Kind, hint string) error {
	err := &hostos.UnsupportedError{Feature: feature, OS: k}
	r, _ := hostos.ReportCommandUnsupported(cmd, err, hint)
	return unsupportedError{err: err, report: r}
}

type unsupportedError struct {
	err    error
	report hostos.CommandReport
}

func (e unsupportedError) Error() string {
	if e.report.Hint == "" {
		return e.err.Error()
	}
	return e.err.Error() + "; " + e.report.Hint
}
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
