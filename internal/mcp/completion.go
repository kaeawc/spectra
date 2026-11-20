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

// pidListing caches one listing of pids and refreshes it in the background,
// so a completion never waits longer than completionBudget.
type pidListing struct {
	mu       sync.Mutex
	entries  []pidEntry
	at       time.Time
	inflight bool
	// done is closed when the in-flight refresh finishes.
	done chan struct{}
}

// get returns the cached listing, refreshing it first when it is older than
// completionTTL. now is the caller's clock; load runs the real collection.
func (l *pidListing) get(ctx context.Context, now time.Time, load func(context.Context) []pidEntry) []pidEntry {
	l.mu.Lock()
	if !l.at.IsZero() && now.Sub(l.at) < completionTTL {
		out := l.entries
		l.mu.Unlock()
		return out
	}
	if !l.inflight {
		l.inflight = true
		done := make(chan struct{})
		l.done = done
		go func() {
			lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			entries := load(lctx)
			l.mu.Lock()
			l.entries, l.at, l.inflight = entries, now, false
			l.mu.Unlock()
			close(done)
		}()
	}
	done := l.done
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
	procs pidListing
	jvms  pidListing
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
	entries := l.get(ctx, s.now(), load)
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

func (s *Server) completeSnapshotIDs(ctx context.Context, prefix string) ([]string, bool) {
	db, err := openStore()
	if err != nil {
		return nil, false
	}
	defer db.Close()
	rows, err := db.ListSnapshots(ctx, "")
	if err != nil {
		return nil, false
	}
	var out []string
	for _, r := range rows {
		if strings.HasPrefix(r.ID, prefix) {
			out = append(out, r.ID)
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
	root, err := s.artifactsRoot()
	if err != nil {
		return nil, false
	}
	entries, err := os.ReadDir(filepath.Join(root, kind))
	if err != nil {
		return nil, false
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && validArtifactID(e.Name()) && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return capValues(out)
}
