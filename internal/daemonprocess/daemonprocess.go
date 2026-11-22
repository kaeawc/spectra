// Package daemonprocess serves the process table from the local daemon, so
// desktop clients that poll it every few seconds skip a CLI spawn per poll.
//
// The hostwatch sampler is not reused: it keeps only aggregates and the top
// CPU consumers, not the full per-process rows `spectra process --json` emits,
// so the rows are collected on demand and shared through a short-TTL cache.
package daemonprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/process"
)

const (
	MethodList      = "process.list"
	MethodSubscribe = "process.subscribe"
	// NotifyUpdate is the notification method streamed to subscribers.
	NotifyUpdate = "process.update"

	// DefaultTTL bounds how stale a cached process.list answer may be.
	DefaultTTL = 2 * time.Second
	// DefaultTimeout bounds one shared collection.
	DefaultTimeout = 15 * time.Second
	// DefaultInterval is the process.update cadence.
	DefaultInterval = 15 * time.Second
)

// CollectFunc collects every process. It should honor ctx.
type CollectFunc func(ctx context.Context) []process.Info

// Snapshot is the process.list result and the process.update payload.
// Processes holds the rows `spectra process --json` prints, in its default
// order (RSS descending).
type Snapshot struct {
	SampledAt time.Time      `json:"sampled_at"`
	Processes []process.Info `json:"processes"`
}

// Options configure a Service. Zero values select production defaults.
type Options struct {
	Collect  CollectFunc
	Clock    clock.Clock
	TTL      time.Duration
	Timeout  time.Duration
	Interval time.Duration
}

type call struct {
	done chan struct{}
	snap Snapshot
	err  error
}

type subscriber struct {
	ch chan Snapshot
}

// Service owns the shared process snapshot and the update stream.
type Service struct {
	collect  CollectFunc
	clock    clock.Clock
	ttl      time.Duration
	timeout  time.Duration
	interval time.Duration

	mu       sync.Mutex
	cached   Snapshot
	valid    bool
	inflight *call
	subs     map[*subscriber]struct{}
	stopLoop context.CancelFunc
}

func NewService(opts Options) *Service {
	s := &Service{collect: opts.Collect, clock: opts.Clock, ttl: opts.TTL, timeout: opts.Timeout,
		interval: opts.Interval, subs: map[*subscriber]struct{}{}}
	if s.collect == nil {
		s.collect = func(ctx context.Context) []process.Info {
			return process.CollectAll(ctx, process.CollectOptions{})
		}
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.ttl <= 0 {
		s.ttl = DefaultTTL
	}
	if s.timeout <= 0 {
		s.timeout = DefaultTimeout
	}
	if s.interval <= 0 {
		s.interval = DefaultInterval
	}
	return s
}

func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodList, svc.listRPC)
	server.Register(MethodSubscribe, svc.subscribeRPC)
}

// List returns the cached snapshot when it is younger than the TTL, otherwise
// collects. Concurrent callers share one collection that is detached from any
// single caller's cancellation but bounded by the timeout. refresh skips the
// cache but joins a collection already in flight, since that one is fresh.
// Failed collections are not cached.
func (s *Service) List(ctx context.Context, refresh bool) (Snapshot, error) {
	s.mu.Lock()
	if !refresh && s.valid && s.clock.Now().Sub(s.cached.SampledAt) < s.ttl {
		snap := s.cached
		s.mu.Unlock()
		return snap, nil
	}
	c := s.inflight
	if c == nil {
		c = &call{done: make(chan struct{})}
		s.inflight = c
		go s.run(context.WithoutCancel(ctx), c)
	}
	s.mu.Unlock()
	select {
	case <-c.done:
		return c.snap, c.err
	case <-ctx.Done():
		return Snapshot{}, fmt.Errorf("process list: %w", ctx.Err())
	}
}

type result struct {
	procs []process.Info
	err   error
}

// run collects on its own goroutine so a collector that ignores ctx still
// releases waiters at the deadline, and a panic becomes an error. inflight is
// always cleared.
func (s *Service) run(base context.Context, c *call) {
	ctx, cancel := context.WithTimeout(base, s.timeout)
	defer cancel()
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("process list: collector panicked: %v", r)}
			}
		}()
		ch <- result{procs: s.collect(ctx)}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
	}
	if r.err == nil && ctx.Err() != nil {
		r.err = fmt.Errorf("process list: collection timed out after %s: %w", s.timeout, ctx.Err())
	}
	var snap Snapshot
	if r.err == nil {
		procs := r.procs
		if procs == nil {
			procs = []process.Info{}
		}
		sortByRSS(procs)
		snap = Snapshot{SampledAt: s.clock.Now(), Processes: procs}
	}
	s.mu.Lock()
	if r.err == nil {
		s.cached, s.valid = snap, true
		c.snap = snap
	}
	c.err = r.err
	s.inflight = nil
	s.mu.Unlock()
	close(c.done)
}

// sortByRSS matches `spectra process`'s default --sort rss.
func sortByRSS(procs []process.Info) {
	sort.Slice(procs, func(i, j int) bool { return rssBytes(procs[i]) > rssBytes(procs[j]) })
}

func rssBytes(p process.Info) uint64 {
	if p.RSSKiB <= 0 {
		return 0
	}
	return uint64(p.RSSKiB) * 1024
}

// Subscribers reports the current subscriber count.
func (s *Service) Subscribers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// Subscribe streams a snapshot to notify on every collection until done
// closes. Collection runs only while at least one subscriber exists: the first
// subscriber starts the loop (which collects immediately, then once per
// interval) and the last one leaving stops it.
func (s *Service) Subscribe(done <-chan struct{}, notify func(string, any) error) {
	sub := &subscriber{ch: make(chan Snapshot, 2)}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	if s.stopLoop == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.stopLoop = cancel
		go s.loop(ctx)
	}
	s.mu.Unlock()
	go func() {
		defer s.unsubscribe(sub)
		for {
			select {
			case <-done:
				return
			case snap := <-sub.ch:
				if err := notify(NotifyUpdate, snap); err != nil {
					return
				}
			}
		}
	}()
}

func (s *Service) unsubscribe(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, sub)
	if len(s.subs) == 0 && s.stopLoop != nil {
		s.stopLoop()
		s.stopLoop = nil
	}
}

func (s *Service) loop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var last time.Time
	for {
		if snap, err := s.List(ctx, false); err == nil && !snap.SampledAt.Equal(last) {
			last = snap.SampledAt
			s.fanout(snap)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) fanout(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.ch <- snap:
		default: // slow client: drop; the next snapshot supersedes it
		}
	}
}

func (s *Service) listRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Refresh bool `json:"refresh"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, req.Done)
	defer cancel()
	return s.List(ctx, p.Refresh)
}

func (s *Service) subscribeRPC(_ context.Context, req *daemon.Request) (any, error) {
	s.Subscribe(req.Done, req.Notify)
	return map[string]bool{"subscribed": true}, nil
}

func requestContext(parent context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if done != nil {
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

func decodeParams(req *daemon.Request, dst any) error {
	if len(req.Params) == 0 || string(req.Params) == "null" {
		return nil
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		return daemon.InvalidParams("invalid params: %v", err)
	}
	return nil
}
