package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/storagestate"
)

var footprintT0 = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

// countingWalk fakes storagestate.CollectFootprint, returning an
// incrementing LibraryBytes so each walk is distinguishable. gate, when set,
// blocks the walk until closed.
type countingWalk struct {
	calls atomic.Int64
	gate  chan struct{}
}

func (w *countingWalk) collect(_ hostos.Kind, _ string, now func() time.Time) storagestate.Footprint {
	n := w.calls.Add(1)
	if w.gate != nil {
		<-w.gate
	}
	return storagestate.Footprint{LibraryBytes: n * 1000, CachesBytes: n, AsOf: now()}
}

func newTTL(t *testing.T, clk *clock.Fake) *cache.TTLStore {
	t.Helper()
	ttl := cache.NewTTLStore(cache.NewShardedStore(t.TempDir(), "storage"), nil)
	ttl.SetClock(clk.Now)
	return ttl
}

func waitIdle(t *testing.T, c *FootprintCache) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		n := len(c.flights)
		c.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("background footprint refresh did not finish")
}

func TestFootprintCacheOneShotWalksOnMissThenServesFresh(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	walk := &countingWalk{}
	ttl := newTTL(t, clk)
	c := &FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}

	fp, stale := c.Footprint(hostos.Darwin, "/home/a", FootprintFreshOnly)
	if stale || fp.LibraryBytes != 1000 || !fp.AsOf.Equal(footprintT0) {
		t.Fatalf("miss: fp=%+v stale=%v", fp, stale)
	}
	clk.Advance(9 * time.Minute)
	if fp, _ = c.Footprint(hostos.Darwin, "/home/a", FootprintFreshOnly); fp.LibraryBytes != 1000 {
		t.Errorf("within 10m should be cached, got walk #%d", fp.LibraryBytes/1000)
	}

	// A new one-shot process sees the persisted entry.
	other := &FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}
	if fp, _ = other.Footprint(hostos.Darwin, "/home/a", FootprintFreshOnly); fp.LibraryBytes != 1000 {
		t.Errorf("persisted entry not reused across caches: %+v", fp)
	}

	clk.Advance(2 * time.Minute) // 11m: expired
	fp, stale = other.Footprint(hostos.Darwin, "/home/a", FootprintFreshOnly)
	if stale || fp.LibraryBytes != 2000 {
		t.Errorf("one-shot expiry must walk in foreground: fp=%+v stale=%v", fp, stale)
	}
	if got := walk.calls.Load(); got != 2 {
		t.Errorf("walks = %d, want 2", got)
	}
}

func TestFootprintCacheServesStaleAndRefreshesOnce(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	walk := &countingWalk{}
	c := &FootprintCache{Clock: clk, Collect: walk.collect}
	c.Footprint(hostos.Darwin, "/h", FootprintServeStale)

	clk.Advance(time.Hour)
	walk.gate = make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fp, stale := c.Footprint(hostos.Darwin, "/h", FootprintServeStale)
			if !stale || fp.LibraryBytes != 1000 {
				t.Errorf("want stale first walk, got fp=%+v stale=%v", fp, stale)
			}
		}()
	}
	wg.Wait() // stale readers return without waiting for the gated walk
	close(walk.gate)
	waitIdle(t, c)
	if got := walk.calls.Load(); got != 2 {
		t.Errorf("walks = %d, want 2 (single-flight refresh)", got)
	}
	fp, stale := c.Footprint(hostos.Darwin, "/h", FootprintServeStale)
	if stale || fp.LibraryBytes != 2000 || !fp.AsOf.Equal(footprintT0.Add(time.Hour)) {
		t.Errorf("after refresh: fp=%+v stale=%v", fp, stale)
	}
}

func TestFootprintCacheTooStaleWalksInForeground(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	walk := &countingWalk{}
	c := &FootprintCache{Clock: clk, Collect: walk.collect}
	c.Footprint(hostos.Darwin, "/h", FootprintServeStale)
	clk.Advance(25 * time.Hour)
	if fp, stale := c.Footprint(hostos.Darwin, "/h", FootprintServeStale); stale || fp.LibraryBytes != 2000 {
		t.Errorf("past max-stale: fp=%+v stale=%v, want fresh walk", fp, stale)
	}
}

func TestFootprintCacheConcurrentMissesShareOneWalk(t *testing.T) {
	walk := &countingWalk{gate: make(chan struct{})}
	c := &FootprintCache{Clock: clock.NewFake(footprintT0), Collect: walk.collect}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Footprint(hostos.Darwin, "/h", FootprintFreshOnly)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(walk.gate)
	wg.Wait()
	if got := walk.calls.Load(); got != 1 {
		t.Errorf("walks = %d, want 1", got)
	}
}

func TestCollectStorageFootprintTTLIndependentOfDF(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	ttl := newTTL(t, clk)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "Library", "Caches"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "Library", "Caches", "blob"), make([]byte, 64_000), 0o644); err != nil {
		t.Fatal(err)
	}
	var dfCalls atomic.Int64
	opts := storagestate.CollectOptions{
		Home: home,
		OS:   hostos.Darwin,
		CmdRunner: func(name string, _ ...string) ([]byte, error) {
			if name == "df" {
				dfCalls.Add(1)
				return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/disk3s1 100 50 50 50% /\n"), nil
			}
			return nil, errors.New("unavailable")
		},
	}
	walk := &countingWalk{}
	fpc := &FootprintCache{Store: ttl, Clock: clk, Collect: walk.collect}

	s := collectStorageCached(opts, ttl, fpc, FootprintFreshOnly, clk)
	if s.UserLibraryBytes != 1000 || !s.UserFootprintAsOf.Equal(footprintT0) || len(s.Volumes) != 1 {
		t.Fatalf("first collect: %+v", s)
	}
	clk.Advance(time.Minute) // df entry (30s) expired, footprint (10m) fresh
	s = collectStorageCached(opts, ttl, fpc, FootprintFreshOnly, clk)
	if dfCalls.Load() != 2 {
		t.Errorf("df calls = %d, want 2 (30s TTL)", dfCalls.Load())
	}
	if walk.calls.Load() != 1 || s.UserLibraryBytes != 1000 {
		t.Errorf("footprint re-walked within its TTL: walks=%d", walk.calls.Load())
	}

	blob, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatal(err)
	}
	if m["user_footprint_as_of"] != "2026-11-21T12:00:00Z" {
		t.Errorf("user_footprint_as_of = %v", m["user_footprint_as_of"])
	}
	if _, ok := m["user_footprint_stale"]; ok {
		t.Error("user_footprint_stale should be omitted when fresh")
	}
}

func TestCollectStorageNoCachesIsLive(t *testing.T) {
	home := t.TempDir()
	s := collectStorageCached(storagestate.CollectOptions{
		Home:      home,
		OS:        hostos.Darwin,
		CmdRunner: func(string, ...string) ([]byte, error) { return nil, errors.New("x") },
		Now:       func() time.Time { return footprintT0 },
	}, nil, nil, FootprintFreshOnly, nil)
	if !s.UserFootprintAsOf.Equal(footprintT0) {
		t.Errorf("live collect should stamp as_of: %v", s.UserFootprintAsOf)
	}
}

func TestFootprintCacheStartReusesFreshResultFromRacingCaller(t *testing.T) {
	clk := clock.NewFake(footprintT0)
	w := &countingWalk{}
	c := &FootprintCache{Clock: clk, Collect: w.collect}
	key := footprintCacheKey(hostos.Darwin, "/Users/user")
	// Another caller finished a walk after this caller's lookup missed.
	c.mem = map[string]storagestate.Footprint{key: {LibraryBytes: 42, AsOf: clk.Now()}}

	f := c.start(key, hostos.Darwin, "/Users/user", false)
	<-f.done
	if f.fp.LibraryBytes != 42 || w.calls.Load() != 0 {
		t.Fatalf("non-forced start walked again: fp=%+v walks=%d", f.fp, w.calls.Load())
	}
	f = c.start(key, hostos.Darwin, "/Users/user", true)
	<-f.done
	if w.calls.Load() != 1 {
		t.Fatalf("forced start must walk, walks=%d", w.calls.Load())
	}
}
