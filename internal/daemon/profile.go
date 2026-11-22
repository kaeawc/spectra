package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"
)

// MethodProfile writes one runtime profile of the daemon into
// <Dir>/profiles and returns its path. It is a built-in like daemon.status:
// it costs nothing until called, needs no listener beyond the owner-only
// Unix socket, and inherits the socket's peer-UID check.
const MethodProfile = "daemon.profile"

const (
	defaultCPUProfileSeconds = 10
	maxCPUProfileSeconds     = 120
	// keepProfiles is how many profiles stay in <Dir>/profiles; older ones
	// are removed after each write.
	keepProfiles = 20
	// partialSuffix marks a capture still being written. Pruning ignores
	// it, so a concurrent capture's prune never deletes an active one; a
	// file left by a crash mid-capture stays until removed by hand.
	partialSuffix = ".partial"
)

// ProfileParams selects the profile. Kind is heap, allocs, goroutine or cpu;
// Seconds only applies to cpu (default 10, at most 120).
type ProfileParams struct {
	Kind    string `json:"kind"`
	Seconds int    `json:"seconds,omitempty"`
}

// ProfileResult names the written pprof file and carries a cheap runtime
// summary taken when the profile finished.
type ProfileResult struct {
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	Seconds      int    `json:"seconds,omitempty"`
	HeapAlloc    uint64 `json:"heap_alloc_bytes"`
	HeapInuse    uint64 `json:"heap_inuse_bytes"`
	HeapObjects  uint64 `json:"heap_objects"`
	Sys          uint64 `json:"sys_bytes"`
	NumGC        uint32 `json:"num_gc"`
	NumGoroutine int    `json:"num_goroutine"`
}

// cpuProfileMu serialises CPU profiles: the runtime allows only one.
var cpuProfileMu sync.Mutex

func (s *Server) profile(ctx context.Context, done <-chan struct{}, raw json.RawMessage) (ProfileResult, error) {
	p, err := parseProfileParams(raw)
	if err != nil {
		return ProfileResult{}, err
	}
	dir := filepath.Join(s.opts.Paths.Dir, "profiles")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ProfileResult{}, fmt.Errorf("daemon.profile: create directory: %w", err)
	}
	stamp := s.opts.Clock.Now().UTC().Format("20060102T150405.000Z")
	// CreateTemp makes the file 0600 and keeps same-millisecond requests
	// apart. The capture is written under a staging name that pruning
	// ignores and renamed once complete.
	f, err := os.CreateTemp(dir, p.Kind+"-"+stamp+"-*.pprof"+partialSuffix)
	if err != nil {
		return ProfileResult{}, fmt.Errorf("daemon.profile: create file: %w", err)
	}
	staging := f.Name()
	path := strings.TrimSuffix(staging, partialSuffix)
	if p.Kind == "cpu" {
		err = writeCPUProfile(ctx, done, f, time.Duration(p.Seconds)*time.Second)
	} else {
		if p.Kind == "heap" {
			runtime.GC() // a heap profile reflects the last GC; make it current
		}
		err = pprof.Lookup(p.Kind).WriteTo(f, 0)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(staging, path)
	}
	if err != nil {
		_ = os.Remove(staging)
		return ProfileResult{}, fmt.Errorf("daemon.profile: %w", err)
	}
	if err := pruneProfiles(dir, keepProfiles); err != nil {
		s.opts.Logger.Warn("daemon.profile: prune old profiles", "error", err)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ProfileResult{Kind: p.Kind, Path: path, Seconds: p.Seconds, HeapAlloc: ms.HeapAlloc, HeapInuse: ms.HeapInuse, HeapObjects: ms.HeapObjects, Sys: ms.Sys, NumGC: ms.NumGC, NumGoroutine: runtime.NumGoroutine()}, nil
}

func parseProfileParams(raw json.RawMessage) (ProfileParams, error) {
	var p ProfileParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &p); err != nil {
			return ProfileParams{}, InvalidParams("daemon.profile: %v", err)
		}
	}
	switch p.Kind {
	case "heap", "allocs", "goroutine":
		if p.Seconds != 0 {
			return ProfileParams{}, InvalidParams("daemon.profile: seconds applies only to kind cpu")
		}
	case "cpu":
		if p.Seconds == 0 {
			p.Seconds = defaultCPUProfileSeconds
		}
		if p.Seconds < 1 || p.Seconds > maxCPUProfileSeconds {
			return ProfileParams{}, InvalidParams("daemon.profile: seconds must be 1..%d", maxCPUProfileSeconds)
		}
	default:
		return ProfileParams{}, InvalidParams("daemon.profile: kind must be heap, allocs, goroutine or cpu")
	}
	return p, nil
}

// firstErrWriter records the first write error: the runtime's CPU profile
// writer discards them, so a full disk would otherwise go unnoticed.
type firstErrWriter struct {
	w   io.Writer
	mu  sync.Mutex
	err error
}

func (f *firstErrWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if err != nil {
		f.mu.Lock()
		if f.err == nil {
			f.err = err
		}
		f.mu.Unlock()
	}
	return n, err
}

func (f *firstErrWriter) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func writeCPUProfile(ctx context.Context, done <-chan struct{}, w io.Writer, d time.Duration) error {
	if !cpuProfileMu.TryLock() {
		return fmt.Errorf("a CPU profile is already running")
	}
	defer cpuProfileMu.Unlock()
	fw := &firstErrWriter{w: w}
	if err := pprof.StartCPUProfile(fw); err != nil {
		return err
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	case <-done:
	}
	pprof.StopCPUProfile() // flushes the remaining profile before returning
	return fw.Err()
}

// pruneProfiles removes all but the newest keep .pprof files in dir, newest
// by modification time (then name). Staging .pprof.partial files are not
// counted or removed.
func pruneProfiles(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type profile struct {
		name string
		mod  time.Time
	}
	var profiles []profile
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".pprof") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed meanwhile
		}
		profiles = append(profiles, profile{e.Name(), info.ModTime()})
	}
	if len(profiles) <= keep {
		return nil
	}
	sort.Slice(profiles, func(i, j int) bool {
		if !profiles[i].mod.Equal(profiles[j].mod) {
			return profiles[i].mod.After(profiles[j].mod)
		}
		return profiles[i].name > profiles[j].name
	})
	var firstErr error
	for _, p := range profiles[keep:] {
		if err := os.Remove(filepath.Join(dir, p.name)); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
