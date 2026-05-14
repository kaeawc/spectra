package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
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

// collectStorageCached mirrors collectToolchainCached for storagestate.
func collectStorageCached(opts storagestate.CollectOptions, ttl *cache.TTLStore) storagestate.State {
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
		"storage",
		opts.Home,
		strings.Join(opts.AppPaths, "|"),
	}
	h := sha256.New()
	h.Write([]byte(strings.Join(parts, "\x00")))
	return h.Sum(nil)
}
