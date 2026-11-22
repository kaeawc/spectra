// Package daemonjvm serves JVM inventory and per-JVM rule findings from the
// local daemon, so MCP and desktop clients skip a cold CLI collection.
package daemonjvm

import (
	"context"
	"encoding/json"
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
)

// ListFunc collects the running JVMs.
type ListFunc func(ctx context.Context) []jvm.Info

// SnapshotFunc builds the snapshot jvm.findings evaluates rules against.
type SnapshotFunc func(ctx context.Context) snapshot.Snapshot

// Options configure a Service. Zero values select production defaults.
type Options struct {
	List     ListFunc
	Snapshot SnapshotFunc
	Catalog  []rules.Rule
	Clock    clock.Clock
	TTL      time.Duration
}

// Service owns the jvm.list cache and the jvm.findings evaluation.
type Service struct {
	list     ListFunc
	snapshot SnapshotFunc
	catalog  []rules.Rule
	clock    clock.Clock
	ttl      time.Duration

	mu       sync.Mutex
	cached   []jvm.Info
	cachedAt time.Time
	valid    bool
	inflight *listCall
}

type listCall struct {
	done  chan struct{}
	infos []jvm.Info
}

// FindingsResult is the jvm.findings reply.
type FindingsResult struct {
	PID      int             `json:"pid"`
	Findings []rules.Finding `json:"findings"`
}

func NewService(opts Options) *Service {
	s := &Service{list: opts.List, snapshot: opts.Snapshot, catalog: opts.Catalog, clock: opts.Clock, ttl: opts.TTL}
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
	return s
}

// DefaultList matches `spectra jvm -json`: JDK attribution plus the shared
// static-facts cache.
func DefaultList(ctx context.Context) []jvm.Info {
	return jvm.CollectAll(ctx, jvm.CollectOptions{
		JDKs:        toolchain.CollectJDKs(ctx, toolchain.CollectOptions{}),
		StaticCache: jvm.DefaultStaticCache,
	})
}

// DefaultSnapshot builds a host snapshot without the slow app, storage,
// services, and update collectors; processes, toolchains, JVMs, and host
// facts — what the per-JVM rules read — stay in.
func DefaultSnapshot(version string) SnapshotFunc {
	return func(ctx context.Context) snapshot.Snapshot {
		return snapshot.Build(ctx, snapshot.Options{
			SpectraVersion: version,
			SkipApps:       true,
			SkipStorage:    true,
			SkipServices:   true,
			SkipUpdates:    true,
		})
	}
}

func RegisterMethods(server *daemon.Server, svc *Service) {
	server.Register(MethodList, svc.listRPC)
	server.Register(MethodFindings, svc.findingsRPC)
}

// List returns the cached JVM list when it is younger than the TTL, otherwise
// collects. Concurrent callers share one collection, which runs detached from
// any single caller's cancellation. refresh skips the cache but still joins a
// collection already in flight, since that one is fresh.
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
		return call.infos, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("jvm list: %w", ctx.Err())
	}
}

func (s *Service) collect(ctx context.Context, call *listCall) {
	infos := s.list(ctx)
	if infos == nil {
		infos = []jvm.Info{}
	}
	s.mu.Lock()
	s.cached, s.cachedAt, s.valid = infos, s.clock.Now(), true
	s.inflight = nil
	call.infos = infos
	s.mu.Unlock()
	close(call.done)
}

// Findings evaluates the catalog against a fresh snapshot and keeps only the
// findings about pid.
func (s *Service) Findings(ctx context.Context, pid int) (FindingsResult, error) {
	if pid <= 0 {
		return FindingsResult{}, daemon.InvalidParams("positive pid required")
	}
	snap := s.snapshot(ctx)
	if !hasJVM(snap, pid) {
		return FindingsResult{}, fmt.Errorf("pid %d is not a running JVM", pid)
	}
	out := FindingsResult{PID: pid, Findings: []rules.Finding{}}
	for _, f := range rules.Evaluate(snap, s.catalog) {
		if subjectIsPID(f.Subject, pid) {
			out.Findings = append(out.Findings, f)
		}
	}
	return out, nil
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
	return s.List(ctx, p.Refresh)
}

func (s *Service) findingsRPC(ctx context.Context, req *daemon.Request) (any, error) {
	var p struct {
		PID int `json:"pid"`
	}
	if err := decodeParams(req, &p); err != nil {
		return nil, err
	}
	return s.Findings(ctx, p.PID)
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
