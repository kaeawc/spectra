package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
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
	// CreateTemp makes the file 0600 and keeps same-millisecond requests apart.
	f, err := os.CreateTemp(dir, p.Kind+"-"+stamp+"-*.pprof")
	if err != nil {
		return ProfileResult{}, fmt.Errorf("daemon.profile: create file: %w", err)
	}
	path := f.Name()
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
	if err != nil {
		_ = os.Remove(path)
		return ProfileResult{}, fmt.Errorf("daemon.profile: %w", err)
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

func writeCPUProfile(ctx context.Context, done <-chan struct{}, f *os.File, d time.Duration) error {
	if !cpuProfileMu.TryLock() {
		return fmt.Errorf("a CPU profile is already running")
	}
	defer cpuProfileMu.Unlock()
	if err := pprof.StartCPUProfile(f); err != nil {
		return err
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	case <-done:
	}
	pprof.StopCPUProfile()
	return nil
}
