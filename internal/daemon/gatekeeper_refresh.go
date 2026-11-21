package daemon

import (
	"context"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/logger"
)

// GatekeeperRefreshOptions configures the background spctl refresher.
type GatekeeperRefreshOptions struct {
	// Apps lists the installed bundles to keep assessed.
	Apps func() []string
	// Detect carries the Runner, SealCache (the persistent verdict store),
	// Gatekeeper assessor and OS used for each assessment. Its Context is
	// replaced by the refresher's.
	Detect detect.Options
	// Interval is the wait between passes; zero means one hour. Verdicts
	// live 24 h, so most passes find nothing stale.
	Interval time.Duration
	// Pause is the wait after each spctl run, so the refresher never keeps
	// syspolicyd busy back to back; zero means five seconds.
	Pause time.Duration
	// Wait sleeps for d or until ctx ends, reporting whether the full
	// duration elapsed. Nil uses a timer; tests inject their own.
	Wait   func(ctx context.Context, d time.Duration) bool
	Logger logger.Logger
}

// GatekeeperRefresher re-assesses installed apps one at a time so a
// Gatekeeper verdict is usually cached before anyone asks for it. Each
// assessment goes through the process-wide serialized assessor with its
// 15 s cap, entries still fresh are skipped without running spctl, and
// cancelling the context stops it between (or during) assessments.
type GatekeeperRefresher struct {
	opts GatekeeperRefreshOptions
}

// NewGatekeeperRefresher fills defaults and returns a refresher.
func NewGatekeeperRefresher(opts GatekeeperRefreshOptions) *GatekeeperRefresher {
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	if opts.Pause <= 0 {
		opts.Pause = 5 * time.Second
	}
	if opts.Wait == nil {
		opts.Wait = sleepCtx
	}
	if opts.Logger == nil {
		opts.Logger = logger.Discard()
	}
	return &GatekeeperRefresher{opts: opts}
}

// Run makes passes until ctx is canceled.
func (r *GatekeeperRefresher) Run(ctx context.Context) {
	for {
		r.Pass(ctx)
		if !r.opts.Wait(ctx, r.opts.Interval) {
			return
		}
	}
}

// Pass assesses every listed app whose verdict is stale, serially, and
// returns how many spctl runs it made. It stops early when ctx ends.
func (r *GatekeeperRefresher) Pass(ctx context.Context) int {
	if r.opts.Apps == nil {
		return 0
	}
	dopts := r.opts.Detect
	dopts.Context = ctx
	assessed := 0
	for _, app := range r.opts.Apps() {
		if ctx.Err() != nil {
			return assessed
		}
		res := detect.RefreshGatekeeper(app, dopts)
		if !res.Assessed {
			continue
		}
		assessed++
		if res.Err != "" {
			r.opts.Logger.Debug("gatekeeper refresh: no verdict", "app", app, "error", res.Err)
		}
		if !r.opts.Wait(ctx, r.opts.Pause) {
			return assessed
		}
	}
	return assessed
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
