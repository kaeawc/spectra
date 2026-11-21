package snapshot

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
)

func TestFootprintCacheRefreshStoresForLaterReads(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	ttl := newTTL(t, clk)
	walk := &countingWalk{}
	(&FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}).Footprint(hostos.Darwin, "/h", FootprintFreshOnly)
	clk.Advance(time.Minute)

	refreshed := (&FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}).Refresh(hostos.Darwin, "/h")
	if refreshed.LibraryBytes != 2000 {
		t.Fatalf("Refresh must walk even when fresh: %+v", refreshed)
	}
	later := &FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}
	if fp, _ := later.Footprint(hostos.Darwin, "/h", FootprintFreshOnly); fp.LibraryBytes != 2000 {
		t.Errorf("plain read after refresh = walk #%d, want the refreshed value", fp.LibraryBytes/1000)
	}
	if walk.calls.Load() != 2 {
		t.Errorf("walks = %d, want 2", walk.calls.Load())
	}
}

func TestFootprintCacheAcceptStaleNoRefresh(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	walk := &countingWalk{}
	c := &FootprintCache{Clock: clk, Collect: walk.collect}
	c.Footprint(hostos.Darwin, "/h", FootprintFreshOnly)
	clk.Advance(3 * time.Hour)
	fp, stale := c.Footprint(hostos.Darwin, "/h", FootprintAcceptStale)
	if !stale || fp.LibraryBytes != 1000 {
		t.Errorf("AcceptStale: fp=%+v stale=%v, want cached+stale", fp, stale)
	}
	waitIdle(t, c)
	if walk.calls.Load() != 1 {
		t.Errorf("AcceptStale must not refresh; walks = %d", walk.calls.Load())
	}
	clk.Advance(22 * time.Hour) // 25h: full miss
	if fp, stale = c.Footprint(hostos.Darwin, "/h", FootprintAcceptStale); stale || fp.LibraryBytes != 2000 {
		t.Errorf("past max-stale: fp=%+v stale=%v, want fresh walk", fp, stale)
	}
}

// A shared long-lived cache still answers fresh-only reads (persisted
// snapshots) with a fresh walk, even when a stale entry is available.
func TestFootprintCacheFreshOnlyIgnoresStaleEntry(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	walk := &countingWalk{}
	c := &FootprintCache{Clock: clk, Collect: walk.collect}
	c.Footprint(hostos.Darwin, "/h", FootprintServeStale)
	clk.Advance(time.Hour)
	fp, stale := c.Footprint(hostos.Darwin, "/h", FootprintFreshOnly)
	if stale || !fp.AsOf.Equal(clk.Now()) {
		t.Errorf("FreshOnly: fp=%+v stale=%v, want a walk at now", fp, stale)
	}
}

func TestFootprintCacheKeyIncludesXDGCacheHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/a")
	k1 := footprintCacheKey(hostos.Linux, "/home/u")
	t.Setenv("XDG_CACHE_HOME", "/b")
	if k2 := footprintCacheKey(hostos.Linux, "/home/u"); k1 == k2 {
		t.Error("key must change with XDG_CACHE_HOME")
	}
}
