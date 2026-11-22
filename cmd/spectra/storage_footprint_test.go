package main

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

// TestSnapshotUsesStorageFootprintCache checks that `spectra snapshot` reads
// the footprint cache `spectra storage` fills, and that --fresh re-walks.
func TestSnapshotUsesStorageFootprintCache(t *testing.T) {
	prev := cacheStores
	t.Cleanup(func() { cacheStores = prev })
	cacheStores = &cache.Stores{Storage: cache.NewShardedStore(t.TempDir(), cache.KindStorage)}
	home := t.TempDir() // empty: a real walk measures zero bytes

	// A previous `spectra storage` run stored a footprint.
	storageRun, policy := cliFootprintCache(false)
	if storageRun == nil || policy != snapshot.FootprintFreshOnly {
		t.Fatalf("storage cache = %v, policy = %v", storageRun, policy)
	}
	storageRun.Collect = func(_ hostos.Kind, _ string, now func() time.Time) storagestate.Footprint {
		return storagestate.Footprint{LibraryBytes: 4242, AsOf: now()}
	}
	storageRun.Footprint(hostos.Darwin, home, policy)

	var opts snapshot.Options
	withCLIFootprintCache(&opts, false)
	if opts.FootprintCache == nil || opts.FootprintPolicy != snapshot.FootprintFreshOnly {
		t.Fatalf("snapshot footprint cache = %v, policy = %v", opts.FootprintCache, opts.FootprintPolicy)
	}
	walked := false
	opts.FootprintCache.Collect = func(_ hostos.Kind, _ string, now func() time.Time) storagestate.Footprint {
		walked = true
		return storagestate.Footprint{AsOf: now()}
	}
	if fp, _ := opts.FootprintCache.Footprint(hostos.Darwin, home, opts.FootprintPolicy); fp.LibraryBytes != 4242 || walked {
		t.Errorf("snapshot footprint = %+v, walked = %v; want the stored entry without a walk", fp, walked)
	}

	withCLIFootprintCache(&opts, true)
	if opts.FootprintPolicy != snapshot.FootprintRefresh {
		t.Errorf("--fresh policy = %v, want FootprintRefresh", opts.FootprintPolicy)
	}
}

func TestCLIFootprintCacheWithoutStores(t *testing.T) {
	prev := cacheStores
	t.Cleanup(func() { cacheStores = prev })
	cacheStores = nil
	if fp, _ := cliFootprintCache(false); fp != nil {
		t.Errorf("cache = %v, want nil without cache stores", fp)
	}
}
