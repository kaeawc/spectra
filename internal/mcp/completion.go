package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxCompletions caps the values one completion returns.
	maxCompletions = 50
	// completionBudget bounds how long one completion waits for a host
	// listing; a slower one answers from the last listing, or empty.
	completionBudget = 150 * time.Millisecond
	// completionTTL is how long a process or JVM listing is reused.
	completionTTL = 2 * time.Second
	// maxDirScan bounds the directory entries read for one completion.
	maxDirScan = 2000
)

// pidEntry is one candidate for a pid argument.
type pidEntry struct {
	pid  int
	name string
}

// listing caches one host listing and refreshes it in the background, so a
// completion never waits longer than completionBudget.
type listing[T any] struct {
	mu       sync.Mutex
	entries  []T
	at       time.Time
	inflight bool
	// done is closed when the in-flight refresh finishes.
	done chan struct{}
	// base, when set, parents refreshes so ending the session cancels them.
	base context.Context
	// wg counts running refresh goroutines so Run can wait for them.
	wg sync.WaitGroup
}

type pidListing = listing[pidEntry]

// refreshLocked starts a background refresh unless one is running and
// returns the channel closed when it finishes.
func (l *listing[T]) refreshLocked(ctx context.Context, now time.Time, load func(context.Context) []T) chan struct{} {
	if !l.inflight {
		l.inflight = true
		done := make(chan struct{})
		l.done = done
		parent := l.base
		if parent == nil {
			parent = context.WithoutCancel(ctx)
		}
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			lctx, cancel := context.WithTimeout(parent, 5*time.Second)
			defer cancel()
			entries := load(lctx)
			l.mu.Lock()
			l.entries, l.at, l.inflight = entries, now, false
			l.mu.Unlock()
			close(done)
		}()
	}
	return l.done
}

// warm starts a refresh without waiting for it, so the first completion
// finds a listing.
func (l *listing[T]) warm(ctx context.Context, now time.Time, load func(context.Context) []T) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at.IsZero() {
		l.refreshLocked(ctx, now, load)
	}
}

// get returns the cached listing, refreshing it first when it is older than
// completionTTL. now is the caller's clock; load runs the real collection.
func (l *listing[T]) get(ctx context.Context, now time.Time, load func(context.Context) []T) []T {
	l.mu.Lock()
	if !l.at.IsZero() && now.Sub(l.at) < completionTTL {
		out := l.entries
		l.mu.Unlock()
		return out
	}
	done := l.refreshLocked(ctx, now, load)
	l.mu.Unlock()
	timer := time.NewTimer(completionBudget)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	case <-ctx.Done():
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entries
}

type completionCache struct {
	procs     pidListing
	jvms      pidListing
	snapshots listing[string]
}

// bind parents every listing's refreshes on ctx.
func (c *completionCache) bind(ctx context.Context) {
	for _, set := range []func(){
		func() { c.procs.mu.Lock(); c.procs.base = ctx; c.procs.mu.Unlock() },
		func() { c.jvms.mu.Lock(); c.jvms.base = ctx; c.jvms.mu.Unlock() },
		func() { c.snapshots.mu.Lock(); c.snapshots.base = ctx; c.snapshots.mu.Unlock() },
	} {
		set()
	}
}

// wait blocks until every refresh goroutine has finished.
func (c *completionCache) wait() {
	c.procs.wg.Wait()
	c.jvms.wg.Wait()
	c.snapshots.wg.Wait()
}

// completionClock times the completion caches: wall time even when tests fix
// the result clock, so cached listings expire.
func (s *Server) completionClock() time.Time { return s.wall().Now() }

// warmCompletions starts the process listing in the background when a
// session starts, bounded like any refresh.
func (s *Server) warmCompletions(ctx context.Context) {
	if s.skipWarm {
		return
	}
	s.completions.procs.warm(ctx, s.completionClock(), s.loadProcesses)
}

func (s *Server) loadProcesses(ctx context.Context) []pidEntry {
	procs := s.collect.Processes.CollectProcesses(ctx, process.CollectOptions{})
	out := make([]pidEntry, 0, len(procs))
	for _, p := range procs {
		out = append(out, pidEntry{pid: p.PID, name: p.Command})
	}
	return out
}

func (s *Server) loadJVMs(ctx context.Context) []pidEntry {
	infos := s.collect.JVMs.CollectJVMs(ctx, jvm.CollectOptions{})
	out := make([]pidEntry, 0, len(infos))
	for _, j := range infos {
		out = append(out, pidEntry{pid: j.PID, name: j.MainClass})
	}
	return out
}

// complete answers completion/complete. Unknown arguments get an empty list.
func (s *Server) complete(ctx context.Context, req *sdk.CompleteRequest) (*sdk.CompleteResult, error) {
	p := req.Params
	values, more := s.completeValues(ctx, p)
	if values == nil {
		values = []string{}
	}
	return &sdk.CompleteResult{Completion: sdk.CompletionResultDetails{Values: values, HasMore: more}}, nil
}

func (s *Server) completeValues(ctx context.Context, p *sdk.CompleteParams) ([]string, bool) {
	name, prefix := p.Argument.Name, p.Argument.Value
	ref := p.Ref
	if ref == nil {
		return nil, false
	}
	if ref.Type == "ref/resource" {
		if ref.URI != artifactURITemplate {
			return nil, false
		}
		return s.completeArtifact(ctx, name, prefix, p.Context)
	}
	switch name {
	case "pid":
		if ref.Type == "ref/prompt" && ref.Name == "jvm-memory-debug" {
			return s.completePIDs(ctx, &s.completions.jvms, s.loadJVMs, prefix)
		}
		return s.completePIDs(ctx, &s.completions.procs, s.loadProcesses, prefix)
	case "roots", "under":
		return completeDirs(prefix)
	case "id_a", "id_b":
		return s.completeSnapshotIDs(ctx, prefix)
	}
	return nil, false
}

func (s *Server) completePIDs(ctx context.Context, l *pidListing, load func(context.Context) []pidEntry, prefix string) ([]string, bool) {
	entries := l.get(ctx, s.completionClock(), load)
	_, numErr := strconv.Atoi(prefix)
	isNum := numErr == nil
	needle := strings.ToLower(prefix)
	var out []string
	for _, e := range entries {
		id := strconv.Itoa(e.pid)
		switch {
		case prefix == "":
		case isNum && strings.HasPrefix(id, prefix):
		case !isNum && strings.Contains(strings.ToLower(e.name), needle):
		default:
			continue
		}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i])
		b, _ := strconv.Atoi(out[j])
		return a < b
	})
	return capValues(out)
}

func capValues(values []string) ([]string, bool) {
	if len(values) > maxCompletions {
		return values[:maxCompletions], true
	}
	return values, false
}

// completeDirs completes directory names for an absolute (or ~-relative)
// prefix. With no prefix it offers the subdirectories of the home directory.
func completeDirs(prefix string) ([]string, bool) {
	if strings.HasPrefix(prefix, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		prefix = home + prefix[1:]
	}
	if prefix == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		prefix = home + string(filepath.Separator)
	}
	if !filepath.IsAbs(prefix) {
		return nil, false
	}
	dir, base := prefix, ""
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		dir, base = filepath.Split(prefix)
	}
	f, err := os.Open(filepath.Clean(dir)) // #nosec G304 -- directory completion is the feature
	if err != nil {
		return nil, false
	}
	defer f.Close()
	entries, _ := f.ReadDir(maxDirScan)
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		full := filepath.Join(dir, name)
		if !isDirEntry(e, full) {
			continue
		}
		out = append(out, full)
	}
	sort.Strings(out)
	return capValues(out)
}

func isDirEntry(e os.DirEntry, full string) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.IsDir()
}

func loadSnapshotIDs(ctx context.Context) []string {
	db, err := openStore()
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.ListSnapshots(ctx, "")
	if err != nil {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func (s *Server) completeSnapshotIDs(ctx context.Context, prefix string) ([]string, bool) {
	var out []string
	for _, id := range s.completions.snapshots.get(ctx, s.completionClock(), loadSnapshotIDs) {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return capValues(out)
}

// completeArtifact completes the {kind} and {id} variables of the artifact
// template; ids depend on the kind resolved earlier.
func (s *Server) completeArtifact(ctx context.Context, name, prefix string, c *sdk.CompleteContext) ([]string, bool) {
	switch name {
	case "kind":
		var out []string
		for _, k := range artifactKinds {
			if strings.HasPrefix(k, prefix) {
				out = append(out, k)
			}
		}
		return out, false
	case "id":
		kind := ""
		if c != nil {
			kind = c.Arguments["kind"]
		}
		if !validArtifactKind(kind) {
			return nil, false
		}
		if kind == kindSnapshot {
			return s.completeSnapshotIDs(ctx, prefix)
		}
		return s.completeArtifactIDs(kind, prefix)
	}
	return nil, false
}

// completeArtifactIDs lists stored artifact ids, newest first (ids start with
// a timestamp).
func (s *Server) completeArtifactIDs(kind, prefix string) ([]string, bool) {
	dir, err := s.artifactKindDir(kind, false)
	if err != nil {
		return nil, false
	}
	f, err := os.Open(dir) // #nosec G304 -- a checked artifacts directory
	if err != nil {
		return nil, false
	}
	entries, _ := f.ReadDir(maxDirScan)
	f.Close()
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && validArtifactID(e.Name()) && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return capValues(out)
}
