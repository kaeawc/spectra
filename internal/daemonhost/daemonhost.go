// Package daemonhost serves the host facts a desktop Machine page polls
// (memory, thermal, power, network, whatswrong) from the local daemon, so
// clients skip a CLI spawn per poll. Each method returns exactly the JSON the
// matching `spectra <cmd> --json` prints, behind a short per-method TTL cache.
// The slow methods (power, whatswrong) also serve a recent stale value at once
// while one background collection refreshes it.
package daemonhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/memstate"
	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/powerreport"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/thermal"
	"github.com/kaeawc/spectra/internal/whatswrong"
)

const (
	MethodMemory  = "memory.current"
	MethodThermal = "thermal.current"
	MethodPower   = "power.current"
	MethodNetwork = "network.current"
	// MethodWhatswrong serves `spectra whatswrong --json`.
	MethodWhatswrong = "whatswrong.current"

	DefaultMemoryTTL     = 2 * time.Second
	DefaultThermalTTL    = 5 * time.Second
	DefaultPowerTTL      = 5 * time.Second
	DefaultNetworkTTL    = 10 * time.Second
	DefaultWhatswrongTTL = 15 * time.Second
	// DefaultPowerStale and DefaultWhatswrongStale are how long past its TTL
	// a value is still served immediately while a background refresh runs.
	// Older values are recollected while the caller waits.
	DefaultPowerStale      = 2 * time.Minute
	DefaultWhatswrongStale = 5 * time.Minute
	// DefaultTimeout bounds one shared collection.
	DefaultTimeout = 20 * time.Second
	// DefaultPowerTimeout bounds a power collection below the 20 second
	// budget desktop clients allow a call, so a cold call under heavy load
	// fails with a clear timeout instead of outliving the client.
	DefaultPowerTimeout = 15 * time.Second
)

// CollectFunc collects one reply payload. It should honor ctx so a timed-out
// collection stops its children. A returned error is never cached.
type CollectFunc func(ctx context.Context) (any, error)

// Options configure a Service. Zero values select production defaults.
type Options struct {
	Memory  CollectFunc
	Thermal CollectFunc
	Power   CollectFunc
	Network CollectFunc
	// Whatswrong defaults to DefaultWhatswrong(nil), which skips host-watch
	// enrichment; the daemon passes its host-watch service.
	Whatswrong CollectFunc

	Clock clock.Clock
	// Timeout bounds every collection when positive; otherwise power uses
	// DefaultPowerTimeout and the rest DefaultTimeout.
	Timeout time.Duration
	// TTLs and stale windows override the per-method defaults when positive.
	MemoryTTL, ThermalTTL, PowerTTL, NetworkTTL, WhatswrongTTL time.Duration
	PowerStale, WhatswrongStale                                time.Duration
}

// Service owns the per-method caches.
type Service struct {
	methods map[string]*cached
}

// NewService builds a Service from opts.
func NewService(opts Options) *Service {
	clk := opts.Clock
	if clk == nil {
		clk = clock.System{}
	}
	pick := func(f, def CollectFunc) CollectFunc {
		if f != nil {
			return f
		}
		return def
	}
	ttl := func(v, def time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return def
	}
	mk := func(name string, f CollectFunc, t, stale, timeout time.Duration) *cached {
		return &cached{name: name, collect: f, ttl: t, stale: stale, timeout: ttl(opts.Timeout, timeout), clock: clk}
	}
	return &Service{methods: map[string]*cached{
		MethodMemory:  mk(MethodMemory, pick(opts.Memory, DefaultMemory), ttl(opts.MemoryTTL, DefaultMemoryTTL), 0, DefaultTimeout),
		MethodThermal: mk(MethodThermal, pick(opts.Thermal, DefaultThermal(nil)), ttl(opts.ThermalTTL, DefaultThermalTTL), 0, DefaultTimeout),
		MethodPower: mk(MethodPower, pick(opts.Power, DefaultPower), ttl(opts.PowerTTL, DefaultPowerTTL),
			ttl(opts.PowerStale, DefaultPowerStale), DefaultPowerTimeout),
		MethodNetwork: mk(MethodNetwork, pick(opts.Network, DefaultNetwork), ttl(opts.NetworkTTL, DefaultNetworkTTL), 0, DefaultTimeout),
		MethodWhatswrong: mk(MethodWhatswrong, pick(opts.Whatswrong, DefaultWhatswrong(nil)), ttl(opts.WhatswrongTTL, DefaultWhatswrongTTL),
			ttl(opts.WhatswrongStale, DefaultWhatswrongStale), DefaultTimeout),
	}}
}

// DefaultMemory matches `spectra memory --json`. memstate reads sysctl and
// mach calls in-process, so it has no child to kill.
func DefaultMemory(context.Context) (any, error) {
	st, err := memstate.Collect()
	if err != nil {
		return nil, err
	}
	return st, nil
}

// DefaultThermal matches `spectra thermal --json` (a single reading with
// samples: 1). c supplies test seams; its Runner defaults to the
// ctx-killable proc.Default.
func DefaultThermal(c *thermal.Collector) CollectFunc {
	if c == nil {
		c = &thermal.Collector{}
	}
	if c.Runner == nil {
		cp := *c
		cp.Runner = proc.Default
		c = &cp
	}
	return func(ctx context.Context) (any, error) {
		r, err := c.Collect(ctx)
		if err != nil {
			return nil, err
		}
		return thermal.WatchReport{Report: r, Samples: 1}, nil
	}
}

// DefaultPower matches `spectra power --json` (the baseline mode) with
// ctx-killable pmset/top children. Its `top -l 2 -s 1` walks every process
// twice a second apart, so a cold call takes 2-3 seconds on an idle Mac and
// can pass 15 under heavy load with thousands of processes.
func DefaultPower(ctx context.Context) (any, error) {
	return PowerFrom(func(ctx context.Context) sysinfo.PowerState {
		return sysinfo.CollectPower(proc.BoundedRunner(ctx))
	}, time.Now)(ctx)
}

// PowerFrom builds the power collector from a state source and clock, for tests.
func PowerFrom(state func(context.Context) sysinfo.PowerState, now func() time.Time) CollectFunc {
	return func(ctx context.Context) (any, error) {
		return powerreport.Baseline(state(ctx), now(), powerreport.DefaultInterval), nil
	}
}

// DefaultNetwork matches `spectra network --json` with ctx-killable children.
func DefaultNetwork(ctx context.Context) (any, error) {
	return netstate.Collect(netstate.ContextRunner(ctx)), nil
}

// DefaultWhatswrong matches `spectra whatswrong --json`. A non-nil watch
// supplies the daemon alerts and trend in-process, as the CLI gets them from
// a running daemon; nil leaves them out, as `--no-daemon` does.
func DefaultWhatswrong(watch whatswrong.LocalWatchSource) CollectFunc {
	deps := whatswrong.DefaultDeps()
	if watch != nil {
		deps.Connect = whatswrong.LocalWatch(watch)
	}
	return WhatswrongFrom(deps)
}

// WhatswrongFrom builds the whatswrong collector from explicit deps, for tests.
func WhatswrongFrom(deps whatswrong.Deps) CollectFunc {
	return func(ctx context.Context) (any, error) {
		return whatswrong.Collect(ctx, deps), nil
	}
}

// RegisterMethods registers every method on server.
func RegisterMethods(server *daemon.Server, svc *Service) {
	for name, m := range svc.methods {
		server.Register(name, m.rpc)
	}
}

// Get returns the named method's payload, from cache when fresh.
func (s *Service) Get(ctx context.Context, method string, refresh bool) (any, error) {
	m, ok := s.methods[method]
	if !ok {
		return nil, fmt.Errorf("daemonhost: unknown method %q", method)
	}
	return m.get(ctx, refresh)
}

type cached struct {
	name    string
	collect CollectFunc
	ttl     time.Duration
	// stale is how long past ttl a value is still served while a background
	// refresh runs; zero disables stale-while-revalidate.
	stale   time.Duration
	timeout time.Duration
	clock   clock.Clock

	mu       sync.Mutex
	value    any
	at       time.Time
	valid    bool
	inflight *call
}

type call struct {
	done  chan struct{}
	value any
	err   error
}

// get serves from cache while younger than the TTL, and within the stale
// window past it returns the old value at once while starting a background
// refresh. Concurrent callers share one collection detached from any single
// caller's cancellation but bounded by the timeout; refresh skips the cache
// yet joins a collection in flight.
func (c *cached) get(ctx context.Context, refresh bool) (any, error) {
	c.mu.Lock()
	if !refresh && c.valid {
		age := c.clock.Now().Sub(c.at)
		if age < c.ttl+c.stale {
			if age >= c.ttl {
				c.startLocked(ctx)
			}
			v := c.value
			c.mu.Unlock()
			return v, nil
		}
	}
	cl := c.startLocked(ctx)
	c.mu.Unlock()
	select {
	case <-cl.done:
		return cl.value, cl.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%s: %w", c.name, ctx.Err())
	}
}

// startLocked returns the collection in flight, starting one if none is.
// c.mu must be held.
func (c *cached) startLocked(ctx context.Context) *call {
	if c.inflight == nil {
		c.inflight = &call{done: make(chan struct{})}
		go c.run(context.WithoutCancel(ctx), c.inflight)
	}
	return c.inflight
}

type result struct {
	value any
	err   error
}

// run collects on its own goroutine so a collector that ignores ctx still
// releases waiters at the deadline and a panic becomes an error. Failures are
// never cached and inflight is always cleared.
func (c *cached) run(base context.Context, cl *call) {
	ctx, cancel := context.WithTimeout(base, c.timeout)
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%s: collector panicked: %v", c.name, r)}
			}
		}()
		v, err := c.collect(ctx)
		ch <- result{value: v, err: err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
	}
	if r.err == nil && ctx.Err() != nil {
		r.err = fmt.Errorf("%s: collection timed out after %s: %w", c.name, c.timeout, ctx.Err())
	}
	c.mu.Lock()
	if r.err == nil {
		c.value, c.at, c.valid = r.value, c.clock.Now(), true
		cl.value = r.value
	}
	cl.err = r.err
	c.inflight = nil
	c.mu.Unlock()
	close(cl.done)
}

func (c *cached) rpc(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Refresh bool `json:"refresh"`
	}
	if len(req.Params) > 0 && string(req.Params) != "null" {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, daemon.InvalidParams("invalid params: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
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
	return c.get(ctx, p.Refresh)
}
