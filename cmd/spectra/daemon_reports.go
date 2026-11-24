package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemonhost"
	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/idle"
	"github.com/kaeawc/spectra/internal/watchclient"
)

// reportCaller sends one report request to the running daemon. ok is false
// when no daemon answered, so the caller measures locally.
type reportCaller func(ctx context.Context, method string, params, out any, timeout time.Duration) (ok bool, err error)

// callDaemonReport dials the per-user daemon. A missing daemon, or one too
// old to know the method, is not an error: the CLI just measures itself.
func callDaemonReport(ctx context.Context, method string, params, out any, timeout time.Duration) (bool, error) {
	if watchclient.Disabled(os.Getenv) {
		return false, nil
	}
	paths, err := daemon.DefaultPaths(os.Getenv, os.UserHomeDir, runtime.GOOS)
	if err != nil || paths.Socket == "" {
		return false, nil
	}
	dctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	c, err := daemonclient.Dial(dctx, paths.Socket)
	cancel()
	if err != nil {
		return false, nil
	}
	defer func() { _ = c.Close() }()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err = c.Call(cctx, method, params, out)
	var rpcErr *daemonclient.RPCError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &rpcErr) && rpcErr.Code == daemon.CodeMethodNotFound, daemonclient.IsUnavailable(err):
		return false, nil
	default:
		return false, err
	}
}

// loadViaDaemon asks the daemon for the load report. Roots are made absolute
// first, since the daemon's working directory is not ours.
func loadViaDaemon(call reportCaller, opts hostload.ReportOptions, stderr io.Writer) (hostload.Report, bool) {
	if call == nil {
		return hostload.Report{}, false
	}
	roots, err := absPaths(opts.Roots)
	if err != nil {
		return hostload.Report{}, false
	}
	opts.Roots = roots
	var rep hostload.Report
	ok, err := call(context.Background(), daemonhost.MethodLoad, daemonhost.LoadParamsFrom(opts), &rep, opts.Sample+15*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "spectra load: daemon: %v; measuring locally\n", err)
	}
	return rep, ok && err == nil
}

// idleViaDaemon asks the daemon for the idle report, excluding this process's
// ancestors and descendants as a local run would.
func idleViaDaemon(call reportCaller, opts idle.ReportOptions, stderr io.Writer) (idle.Report, bool) {
	if call == nil {
		return idle.Report{}, false
	}
	if len(opts.Roots) == 0 && len(opts.PIDs) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			return idle.Report{}, false
		}
		opts.Roots = []string{wd}
	}
	roots, err := absPaths(opts.Roots)
	if err != nil {
		return idle.Report{}, false
	}
	for _, r := range roots {
		// Leave a bad root to the local run, which reports it as usual.
		if fi, err := os.Stat(r); err != nil || !fi.IsDir() {
			return idle.Report{}, false
		}
	}
	opts.Roots = roots
	var rep idle.Report
	ok, err := call(context.Background(), daemonhost.MethodIdle, daemonhost.IdleParamsFrom(opts, os.Getpid()), &rep, opts.Timeout+5*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "spectra idle: daemon: %v; measuring locally\n", err)
	}
	return rep, ok && err == nil
}

func absPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		a, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
