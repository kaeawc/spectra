// Package daemonjvm serves JVM inventory and per-JVM rule findings from the
// local daemon, so MCP and desktop clients skip a cold CLI collection.
package daemonjvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

const (
	MethodList     = "jvm.list"
	MethodFindings = "jvm.findings"

	// DefaultTTL bounds how stale a cached jvm.list answer may be.
	DefaultTTL = 5 * time.Second
	// DefaultListTimeout bounds one shared jvm.list collection.
	DefaultListTimeout = 30 * time.Second
	// DefaultFindingsTimeout bounds the snapshot behind one jvm.findings call.
	DefaultFindingsTimeout = 60 * time.Second
)

// ErrJPSUnavailable reports that JVM discovery could not run, so an empty
// list would not mean "no JVMs".
var ErrJPSUnavailable = errors.New("jps not found in PATH; JVM discovery unavailable")

// ListFunc collects the running JVMs and reports whether jps was available.
// It should honor ctx so a timed-out collection stops its children.
type ListFunc func(ctx context.Context) (infos []jvm.Info, jpsAvailable bool)

// SnapshotFunc builds the snapshot jvm.findings evaluates rules against.
// It should honor ctx so a timed-out call stops its children.
type SnapshotFunc func(ctx context.Context) snapshot.Snapshot

// Options configure a Service. Zero values select production defaults.
type Options struct {
	List            ListFunc
	Snapshot        SnapshotFunc
	Catalog         []rules.Rule
	Clock           clock.Clock
	TTL             time.Duration
	ListTimeout     time.Duration
	FindingsTimeout time.Duration
}

// Service owns the jvm.list cache and the jvm.findings evaluation.
type Service struct {
	list            ListFunc
	snapshot        SnapshotFunc
	catalog         []rules.Rule
	clock           clock.Clock
	ttl             time.Duration
	listTimeout     time.Duration
	findingsTimeout time.Duration

	mu       sync.Mutex
	cached   []jvm.Info
	cachedAt time.Time
	valid    bool
	inflight *listCall
}

type listCall struct {
	done  chan struct{}
	infos []jvm.Info
	err   error
}

// FindingsResult is the jvm.findings reply.
type FindingsResult struct {
	PID      int             `json:"pid"`
	Findings []rules.Finding `json:"findings"`
	// Warnings are the snapshot's degraded-collection notes, if any.
	Warnings []string `json:"warnings,omitempty"`
}

func NewService(opts Options) *Service {
	s := &Service{list: opts.List, snapshot: opts.Snapshot, catalog: opts.Catalog, clock: opts.Clock, ttl: opts.TTL,
		listTimeout: opts.ListTimeout, findingsTimeout: opts.FindingsTimeout}
	if s.list == nil {
		s.list = DefaultList
	}
	if s.snapshot == nil {
		s.snapshot = DefaultSnapshot("")
	}
	if s.catalog == nil {
		s.catalog = rules.V1Catalog()
	}
	if s.clock == nil {
		s.clock = clock.System{}
	}
	if s.ttl <= 0 {
		s.ttl = DefaultTTL
	}
	if s.listTimeout <= 0 {
		s.listTimeout = DefaultListTimeout
	}
	if s.findingsTimeout <= 0 {
		s.findingsTimeout = DefaultFindingsTimeout
	}
	return s
}

// killableJVMOptions gives the JVM collector runners that kill their child
// when ctx ends; the jvm package's plain default ignores ctx.
func killableJVMOptions(ctx context.Context, opts jvm.CollectOptions) jvm.CollectOptions {
	opts.CmdRunner = jvm.ContextRunner(ctx)
	opts.CmdRunnerContext = jvm.DefaultCtxRunner
	return opts
}

// DefaultList matches `spectra jvm -json`: JDK attribution plus the shared
// static-facts cache, with ctx-killable jps/jcmd children.
func DefaultList(ctx context.Context) ([]jvm.Info, bool) {
	return jvm.CollectAllStatus(ctx, killableJVMOptions(ctx, jvm.CollectOptions{
		JDKs:        toolchain.CollectJDKs(ctx, toolchain.CollectOptions{}),
		StaticCache: jvm.DefaultStaticCache,
	}))
}

// DefaultSnapshot builds a host snapshot without the slow app, storage,
// services, and update collectors; processes, toolchains, JVMs, and host
// facts — what the per-JVM rules read — stay in. JVM tools are killed when
// ctx ends.
func DefaultSnapshot(version string) SnapshotFunc {
	return func(ctx context.Context) snapshot.Snapshot {
		jvmOpts := killableJVMOptions(ctx, jvm.CollectOptions{})
		return snapshot.Build(ctx, snapshot.Options{
			SpectraVersion:   version,
			SkipApps:         true,
			SkipStorage:      true,
			SkipServices:     true,
			SkipUpdates:      true,
			JVMOpts:          jvmOpts,
			JVMTelemetryOpts: jvm.TelemetryOptions{CollectOptions: jvmOpts},
		})
	}
}

func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodList, svc.listRPC)
	server.Register(MethodFindings, svc.findingsRPC)
}

// List returns the cached JVM list when it is younger than the TTL, otherwise
// collects. Concurrent callers share one collection, which is detached from
// any single caller's cancellation but bounded by the list timeout. refresh
// skips the cache but still joins a collection already in flight, since that
// one is fresh. Failed collections (timeout, panic, jps missing) are not cached.
func (s *Service) List(ctx context.Context, refresh bool) ([]jvm.Info, error) {
	s.mu.Lock()
	if !refresh && s.valid && s.clock.Now().Sub(s.cachedAt) < s.ttl {
		infos := s.cached
		s.mu.Unlock()
		return infos, nil
	}
	call := s.inflight
	if call == nil {
		call = &listCall{done: make(chan struct{})}
		s.inflight = call
		go s.collect(context.WithoutCancel(ctx), call)
	}
	s.mu.Unlock()
	select {
	case <-call.done:
		return call.infos, call.err
	case <-ctx.Done():
		return nil, fmt.Errorf("jvm list: %w", ctx.Err())
	}
}

type listResult struct {
	infos     []jvm.Info
	available bool
	err       error
}

// collect runs the collector on its own goroutine so a collector that ignores
// ctx still releases waiters at the deadline, and a panic becomes an error
// rather than killing the daemon. inflight is always cleared.
func (s *Service) collect(base context.Context, call *listCall) {
	ctx, cancel := context.WithTimeout(base, s.listTimeout)
	defer cancel()
	ch := make(chan listResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- listResult{err: fmt.Errorf("jvm list: collector panicked: %v", r)}
			}
		}()
		infos, available := s.list(ctx)
		ch <- listResult{infos: infos, available: available}
	}()
	var r listResult
	select {
	case r = <-ch:
	case <-ctx.Done():
	}
	switch {
	case r.err != nil:
	case ctx.Err() != nil:
		// A collector that returned early on the deadline yields partial data.
		r.err = fmt.Errorf("jvm list: collection timed out after %s: %w", s.listTimeout, ctx.Err())
	case !r.available:
		r.err = fmt.Errorf("jvm list: %w", ErrJPSUnavailable)
	}
	if r.err == nil && r.infos == nil {
		r.infos = []jvm.Info{}
	}
	s.mu.Lock()
	if r.err == nil {
		s.cached, s.cachedAt, s.valid = r.infos, s.clock.Now(), true
		call.infos = r.infos
	}
	call.err = r.err
	s.inflight = nil
	s.mu.Unlock()
	close(call.done)
}

// Findings evaluates the catalog against a fresh snapshot, bounded by the
// findings timeout, and keeps only the findings about pid.
func (s *Service) Findings(ctx context.Context, pid int) (FindingsResult, error) {
	if pid <= 0 {
		return FindingsResult{}, daemon.InvalidParams("positive pid required")
	}
	snap, err := s.buildSnapshot(ctx)
	if err != nil {
		return FindingsResult{}, err
	}
	if !hasJVM(snap, pid) {
		if len(snap.Warnings) > 0 {
			return FindingsResult{}, fmt.Errorf("pid %d is not a running JVM (%s)", pid, strings.Join(snap.Warnings, "; "))
		}
		return FindingsResult{}, fmt.Errorf("pid %d is not a running JVM", pid)
	}
	out := FindingsResult{PID: pid, Findings: []rules.Finding{}, Warnings: snap.Warnings}
	for _, f := range rules.Evaluate(snap, s.catalog) {
		if subjectIsPID(f.Subject, pid) {
			out.Findings = append(out.Findings, f)
		}
	}
	return out, nil
}

type snapshotResult struct {
	snap snapshot.Snapshot
	err  error
}

// buildSnapshot runs the snapshot off the handler goroutine so a collector
// that ignores ctx cannot hold the request past its deadline, and so a panic
// becomes an error instead of killing the daemon.
func (s *Service) buildSnapshot(parent context.Context) (snapshot.Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, s.findingsTimeout)
	defer cancel()
	ch := make(chan snapshotResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- snapshotResult{err: fmt.Errorf("jvm findings: snapshot panicked: %v", r)}
			}
		}()
		ch <- snapshotResult{snap: s.snapshot(ctx)}
	}()
	select {
	case r := <-ch:
		if r.err == nil && ctx.Err() != nil {
			r.err = fmt.Errorf("jvm findings: snapshot: %w", ctx.Err())
		}
		return r.snap, r.err
	case <-ctx.Done():
		return snapshot.Snapshot{}, fmt.Errorf("jvm findings: snapshot: %w", ctx.Err())
	}
}

func hasJVM(snap snapshot.Snapshot, pid int) bool {
	for _, j := range snap.JVMs {
		if j.PID == pid {
			return true
		}
	}
	return false
}

// subjectIsPID matches the rule Subject conventions that name a process:
// "PID <n>" optionally followed by a space and detail (per-JVM rules,
// fd-pressure), and "<name> pid <n>" (process-memory-hog).
func subjectIsPID(subject string, pid int) bool {
	n := strconv.Itoa(pid)
	prefix := "PID " + n
	return subject == prefix || strings.HasPrefix(subject, prefix+" ") || strings.HasSuffix(subject, " pid "+n)
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

func (s *Service) findingsRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		PID int `json:"pid"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, req.Done)
	defer cancel()
	return s.Findings(ctx, p.PID)
}

// requestContext ends when the client connection closes (done), so a waiter
// stops holding a server slot for a caller that is gone.
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
