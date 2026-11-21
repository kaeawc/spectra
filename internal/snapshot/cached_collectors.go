package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/storagestate"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// Default TTLs for the slow but slow-changing collectors.
const (
	toolchainCacheTTL = 5 * time.Minute
	storageCacheTTL   = 30 * time.Second
)

// collectToolchainCached returns toolchain.Toolchains, serving from cache
// when fresh and falling through to the live collector on miss.
//
// The cache key is content-addressed by the input paths so distinct test
// homes / brew roots / JDK roots don't share an entry. Cache writes go
// through the TTLStore's AsyncWriter when one was configured, so they
// never block the main collection loop.
func collectToolchainCached(ctx context.Context, opts toolchain.CollectOptions, ttl *cache.TTLStore) toolchain.Toolchains {
	if ttl == nil {
		return toolchain.Collect(ctx, opts)
	}
	key := toolchainCacheKey(opts)
	if blob, ok := ttl.Get(key); ok {
		var t toolchain.Toolchains
		if err := json.Unmarshal(blob, &t); err == nil {
			return t
		}
	}
	t := toolchain.Collect(ctx, opts)
	if blob, err := json.Marshal(t); err == nil {
		_ = ttl.Put(key, blob, toolchainCacheTTL)
	}
	return t
}

// CollectStorage gathers storagestate.State with the volume/mount data and
// the user footprint cached separately: ttl (may be nil) holds the former
// for storageCacheTTL, fp (may be nil) the latter on its own longer window.
// With both nil it is a plain live collection. With only ttl set, the
// footprint is cached in ttl's backing store. policy governs how old a
// cached footprint may be.
func CollectStorage(opts storagestate.CollectOptions, ttl *cache.TTLStore, fp *FootprintCache, policy FootprintPolicy) storagestate.State {
	return collectStorageCached(opts, ttl, fp, policy, nil)
}

func collectStorageCached(opts storagestate.CollectOptions, ttl *cache.TTLStore, fp *FootprintCache, policy FootprintPolicy, clk clock.Clock) storagestate.State {
	if ttl == nil && fp == nil {
		return storagestate.Collect(opts)
	}
	if fp == nil {
		fp = &FootprintCache{Store: ttl, Clock: clk}
	}
	if opts.Home == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	var (
		footprint storagestate.Footprint
		stale     bool
		done      = make(chan struct{})
	)
	go func() {
		defer close(done)
		footprint, stale = fp.Footprint(opts.OS, opts.Home, policy)
	}()
	base := opts
	base.SkipUserFootprint = true
	s := collectStorageBase(base, ttl)
	<-done
	s.ApplyFootprint(footprint, stale)
	return s
}

// collectStorageBase serves the footprint-free part of storagestate.State
// from ttl when fresh.
func collectStorageBase(opts storagestate.CollectOptions, ttl *cache.TTLStore) storagestate.State {
	if ttl == nil {
		return storagestate.Collect(opts)
	}
	key := storageCacheKey(opts)
	if blob, ok := ttl.Get(key); ok {
		var s storagestate.State
		if err := json.Unmarshal(blob, &s); err == nil {
			return s
		}
	}
	s := storagestate.Collect(opts)
	if blob, err := json.Marshal(s); err == nil {
		_ = ttl.Put(key, blob, storageCacheTTL)
	}
	return s
}

// toolchainCacheKey hashes the cache-relevant inputs (path roots) so distinct
// configurations don't collide. Output bytes are a SHA-256 digest.
func toolchainCacheKey(opts toolchain.CollectOptions) []byte {
	parts := []string{
		"toolchain",
		opts.Home,
		strings.Join(opts.BrewCellars, "|"),
		opts.SystemJVMRoot,
		opts.UserJVMRoot,
	}
	h := sha256.New()
	h.Write([]byte(strings.Join(parts, "\x00")))
	return h.Sum(nil)
}

func storageCacheKey(opts storagestate.CollectOptions) []byte {
	parts := []string{
		// v2 entries exclude the user footprint (cached separately).
		"storage-v2",
		fmt.Sprint(int(hostos.Resolve(opts.OS))),
		opts.Home,
		strings.Join(opts.AppPaths, "|"),
		fmt.Sprint(opts.LargestAppsN),
		fmt.Sprint(opts.IncludeSnapshots),
		fmt.Sprint(opts.IncludeSpotlight),
	}
	h := sha256.New()
	h.Write([]byte(strings.Join(parts, "\x00")))
	return h.Sum(nil)
}
