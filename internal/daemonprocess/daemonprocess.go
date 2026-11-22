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
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/classify"
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/proc"
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

// CollectFunc collects every process. It should honor ctx. An error means the
// collection failed and must not be served or cached.
type CollectFunc func(ctx context.Context) ([]process.Info, error)

// ErrNoProcesses reports an empty collection, which on a live host means ps
// (or procfs) failed rather than that nothing is running.
var ErrNoProcesses = errors.New("process list: collection returned no processes")

// DefaultCollect runs process.CollectAll with a ctx-killable runner, so a
// timed-out collection kills its ps children. base supplies test seams.
func DefaultCollect(base process.CollectOptions) CollectFunc {
	if base.Runner == nil {
		base.Runner = proc.Default
	}
	return func(ctx context.Context) ([]process.Info, error) {
		procs := process.CollectAll(ctx, base)
		if len(procs) == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, ErrNoProcesses
		}
		return procs, nil
	}
}

// ClassifyFunc fills the --classify fields (role, runtime, app, signing,
// evidence) on procs in place, like process.Classify. An error means the rest
// were left unknown; it is reported as a warning, not a failed call.
type ClassifyFunc func(ctx context.Context, procs []process.Info) error

const (
	// ClassifierMaxAge bounds how long one classifier (whose per-executable
	// and per-bundle memo maps never expire) is reused before a fresh one
	// replaces it. The on-disk store keeps the replacement warm.
	ClassifierMaxAge = 10 * time.Minute
	// classifyGrace is how long a timed-out classification may still finish
	// the in-flight work it returns partial results from.
	classifyGrace = 2 * time.Second
)

// rotatingClassifier hands out a classifier that is rebuilt once it is older
// than maxAge, bounding its memo maps and picking up updated app bundles.
type rotatingClassifier struct {
	build  func() *classify.Classifier
	clock  clock.Clock
	maxAge time.Duration

	mu    sync.Mutex
	c     *classify.Classifier
	built time.Time
}

func (r *rotatingClassifier) current() *classify.Classifier {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now := r.clock.Now(); r.c == nil || now.Sub(r.built) >= r.maxAge {
		r.c, r.built = r.build(), now
	}
	return r.c
}

// DefaultClassify returns a ClassifyFunc backed by the on-disk classify cache
// when the cache root is available (nil store otherwise), as
// `spectra process --classify` does. The classifier is reused and rebuilt
// every ClassifierMaxAge.
func DefaultClassify() ClassifyFunc {
	r := &rotatingClassifier{clock: clock.System{}, maxAge: ClassifierMaxAge, build: func() *classify.Classifier {
		opts := classify.Options{}
		if root, err := cache.DefaultRoot(); err == nil {
			opts.Store = cache.NewShardedStore(root, cache.KindClassify).WithMaxAge(cache.ClassifyMaxAge)
		}
		return classify.New(opts)
	}}
	return func(ctx context.Context, procs []process.Info) error {
		return process.Classify(ctx, procs, r.current())
	}
}

// Snapshot is the process.list result and the process.update payload.
// Processes holds the rows `spectra process --json` prints, in its default
// order (RSS descending), with the --classify fields when classified.
// Warning is set only when classification failed or timed out and the
// remaining rows were left unknown.
type Snapshot struct {
	SampledAt time.Time      `json:"sampled_at"`
	Processes []process.Info `json:"processes"`
	Warning   string         `json:"warning,omitempty"`
}

// Options configure a Service. Zero values select production defaults.
type Options struct {
	Collect CollectFunc
	// Classify enriches rows for classify requests. Defaults to
	// DefaultClassify, constructed on first use.
	Classify ClassifyFunc
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
	ch       chan Snapshot
	classify bool
}

// slot is one cached variant (plain or classified) with its single-flight.
type slot struct {
	cached Snapshot
	// at is when the variant finished building; freshness is measured from
	// it, so slow classification does not eat the TTL.
	at       time.Time
	valid    bool
	inflight *call
}

// Service owns the shared process snapshot and the update stream.
type Service struct {
	collect  CollectFunc
	classify ClassifyFunc
	clsOnce  sync.Once
	grace    time.Duration
	clock    clock.Clock
	ttl      time.Duration
	timeout  time.Duration
	interval time.Duration

	mu       sync.Mutex
	plain    slot
	classed  slot
	subs     map[*subscriber]struct{}
	stopLoop context.CancelFunc
}

func NewService(opts Options) *Service {
	s := &Service{collect: opts.Collect, classify: opts.Classify, grace: classifyGrace, clock: opts.Clock, ttl: opts.TTL, timeout: opts.Timeout,
		interval: opts.Interval, subs: map[*subscriber]struct{}{}}
	if s.collect == nil {
		s.collect = DefaultCollect(process.CollectOptions{})
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
	return s.get(ctx, refresh, false, nil)
}

// ListClassified is List with the --classify fields filled. The classified
// snapshot is cached and single-flighted separately from the plain one and is
// built from the latest plain collection, so a plain caller never pays for
// classification and a classified one never sees stale classification.
func (s *Service) ListClassified(ctx context.Context, refresh bool) (Snapshot, error) {
	return s.get(ctx, refresh, true, nil)
}

// classifyFrom classifies exactly plain (the subscribe loop's snapshot), so
// both variants of a tick share one collection and sampled_at.
func (s *Service) classifyFrom(ctx context.Context, plain Snapshot) (Snapshot, error) {
	return s.get(ctx, false, true, &plain)
}

// get serves a variant. from, for classified only, pins the plain snapshot
// to classify instead of fetching the latest one.
func (s *Service) get(ctx context.Context, refresh, classified bool, from *Snapshot) (Snapshot, error) {
	sl := &s.plain
	if classified {
		sl = &s.classed
	}
	s.mu.Lock()
	if !refresh && sl.valid && (s.clock.Now().Sub(sl.at) < s.ttl || (from != nil && sl.cached.SampledAt.Equal(from.SampledAt))) {
		snap := sl.cached
		s.mu.Unlock()
		return snap, nil
	}
	c := sl.inflight
	if c == nil {
		c = &call{done: make(chan struct{})}
		sl.inflight = c
		base := context.WithoutCancel(ctx)
		if classified {
			go s.runClassified(base, c, refresh, from)
		} else {
			go s.run(base, c)
		}
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
		procs, err := s.collect(ctx)
		ch <- result{procs: procs, err: err}
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
		sortByRSS(procs)
		snap = Snapshot{SampledAt: s.clock.Now(), Processes: procs}
	}
	s.finish(&s.plain, c, snap, r.err)
}

// runClassified classifies a copy of the latest plain snapshot (the plain
// cache is never mutated). A collection failure fails the call; a classifier
// error, timeout or panic keeps the rows and sets Warning.
func (s *Service) runClassified(base context.Context, c *call, refresh bool, from *Snapshot) {
	var plain Snapshot
	if from != nil {
		plain = *from
	} else {
		var err error
		if plain, err = s.get(base, refresh, false, nil); err != nil {
			s.finish(&s.classed, c, Snapshot{}, err)
			return
		}
	}
	procs, werr := s.classifyBounded(base, plain.Processes)
	snap := Snapshot{SampledAt: plain.SampledAt, Processes: procs}
	if werr != nil {
		snap.Warning = fmt.Sprintf("classify: %v (remaining processes left unknown)", werr)
	}
	s.finish(&s.classed, c, snap, nil)
}

// classifyBounded classifies a copy of rows on its own goroutine under the
// timeout; a panic becomes an error. After an error the classifier's partial
// results are returned. After the timeout the classifier gets a short grace
// to hand back what it finished (it waits for in-flight codesign calls); only
// if that also expires, the goroutine may still be writing its copy, so the
// untouched rows are returned instead.
func (s *Service) classifyBounded(base context.Context, rows []process.Info) ([]process.Info, error) {
	ctx, cancel := context.WithTimeout(base, s.timeout)
	defer cancel()
	s.clsOnce.Do(func() {
		if s.classify == nil {
			s.classify = DefaultClassify()
		}
	})
	work := slices.Clone(rows)
	ch := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- fmt.Errorf("classifier panicked: %v", r)
			}
		}()
		ch <- s.classify(ctx, work)
	}()
	select {
	case err := <-ch:
		return work, err
	case <-ctx.Done():
	}
	terr := fmt.Errorf("timed out after %s: %w", s.timeout, ctx.Err())
	select {
	case err := <-ch:
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			terr = fmt.Errorf("%w; classifier: %w", terr, err)
		}
		return work, terr
	case <-time.After(s.grace):
		return slices.Clone(rows), terr
	}
}

func (s *Service) finish(sl *slot, c *call, snap Snapshot, err error) {
	s.mu.Lock()
	if err == nil {
		sl.cached, sl.valid, sl.at = snap, true, s.clock.Now()
		c.snap = snap
	}
	c.err = err
	sl.inflight = nil
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
	s.SubscribeClassified(done, false, notify)
}

// SubscribeClassified is Subscribe with the classified variant when
// classified is true. Each tick classifies once, and only if some current
// subscriber asked for it.
func (s *Service) SubscribeClassified(done <-chan struct{}, classified bool, notify func(string, any) error) {
	sub := &subscriber{ch: make(chan Snapshot, 2), classify: classified}
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
	var lastPlain, lastClass time.Time
	for {
		wantPlain, wantClass := s.wanted()
		var plain, class *Snapshot
		if wantPlain || wantClass {
			if base, err := s.List(ctx, false); err == nil {
				if wantPlain && !base.SampledAt.Equal(lastPlain) {
					lastPlain, plain = base.SampledAt, &base
				}
				if wantClass && !base.SampledAt.Equal(lastClass) {
					if snap, err := s.classifyFrom(ctx, base); err == nil {
						lastClass, class = snap.SampledAt, &snap
					}
				}
			}
		}
		s.fanout(plain, class)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// wanted reports which variants the current subscribers need.
func (s *Service) wanted() (plain, classified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		if sub.classify {
			classified = true
		} else {
			plain = true
		}
	}
	return plain, classified
}

func (s *Service) fanout(plain, classified *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		snap := plain
		if sub.classify {
			snap = classified
		}
		if snap == nil {
			continue
		}
		select {
		case sub.ch <- *snap:
		default: // slow client: drop; the next snapshot supersedes it
		}
	}
}

func (s *Service) listRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Refresh  bool `json:"refresh"`
		Classify bool `json:"classify"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, req.Done)
	defer cancel()
	if p.Classify {
		return s.ListClassified(ctx, p.Refresh)
	}
	return s.List(ctx, p.Refresh)
}

func (s *Service) subscribeRPC(_ context.Context, req *daemon.Request) (any, error) {
	var p struct {
		Classify bool `json:"classify"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	s.SubscribeClassified(req.Done, p.Classify, req.Notify)
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
