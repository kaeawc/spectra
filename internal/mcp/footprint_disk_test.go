package mcp

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/storagestate"
)

type fakeFootprintClock struct{ t time.Time }

func (c *fakeFootprintClock) Now() time.Time { return c.t }

// footprintHarness builds server-instance caches over one shared on-disk
// store, with an injected clock and a counting fake walker.
type footprintHarness struct {
	t     *testing.T
	store *cache.TTLStore
	clk   *fakeFootprintClock
	walks atomic.Int32
}

func newFootprintHarness(t *testing.T) *footprintHarness {
	clk := &fakeFootprintClock{t: time.Now()}
	store := cache.NewTTLStore(cache.NewShardedStore(t.TempDir(), cache.KindStorage), nil)
	store.SetClock(clk.Now)
	return &footprintHarness{t: t, store: store, clk: clk}
}

// instance simulates a freshly started server: empty memory, same disk.
func (h *footprintHarness) instance() *snapshot.FootprintCache {
	c := newFootprintCache(h.store)
	c.Clock = h.clk
	c.Collect = func(_ hostos.Kind, _ string, now func() time.Time) storagestate.Footprint {
		h.walks.Add(1)
		return storagestate.Footprint{LibraryBytes: 42, AsOf: now()}
	}
	return c
}

func (h *footprintHarness) waitForDiskAsOf(want time.Time) {
	h.t.Helper()
	for i := 0; i < 500; i++ {
		c := newFootprintCache(h.store)
		c.Clock = h.clk
		c.Collect = func(hostos.Kind, string, func() time.Time) storagestate.Footprint {
			return storagestate.Footprint{}
		}
		if fp, _ := c.Footprint(hostos.Darwin, "/h", snapshot.FootprintAcceptStale); fp.AsOf.Equal(want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("disk entry never updated")
}

func TestFootprintDiskFreshEntryNoWalkOnRestart(t *testing.T) {
	h := newFootprintHarness(t)
	h.instance().Refresh(hostos.Darwin, "/h")
	h.walks.Store(0)

	fp, stale := h.instance().Footprint(hostos.Darwin, "/h", snapshot.FootprintServeStale)
	if stale || fp.LibraryBytes != 42 || h.walks.Load() != 0 {
		t.Fatalf("fp=%+v stale=%v walks=%d, want fresh disk hit with no walk", fp, stale, h.walks.Load())
	}
}

func TestFootprintDiskStaleEntryServedThenRefreshedInBackground(t *testing.T) {
	h := newFootprintHarness(t)
	h.instance().Refresh(hostos.Darwin, "/h")
	old := h.clk.t
	h.walks.Store(0)
	h.clk.t = old.Add(time.Hour)

	fp, stale := h.instance().Footprint(hostos.Darwin, "/h", snapshot.FootprintServeStale)
	if !stale || !fp.AsOf.Equal(old) {
		t.Fatalf("fp.AsOf=%v stale=%v, want old entry marked stale", fp.AsOf, stale)
	}
	h.waitForDiskAsOf(h.clk.t)
	if n := h.walks.Load(); n != 1 {
		t.Fatalf("walks = %d, want exactly 1 background refresh", n)
	}
}

func TestFootprintDiskMissWalksOnceThenPersists(t *testing.T) {
	h := newFootprintHarness(t)
	if _, stale := h.instance().Footprint(hostos.Darwin, "/h", snapshot.FootprintServeStale); stale || h.walks.Load() != 1 {
		t.Fatalf("stale=%v walks=%d, want one foreground walk", stale, h.walks.Load())
	}
	h.waitForDiskAsOf(h.clk.t)
	h.instance().Footprint(hostos.Darwin, "/h", snapshot.FootprintServeStale)
	if n := h.walks.Load(); n != 1 {
		t.Fatalf("walks = %d after restart, want still 1", n)
	}
}
