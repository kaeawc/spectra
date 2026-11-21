package snapshot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/storagestate"
)

// Footprint cache windows. The ~/Library walk is the slowest part of storage
// collection and its totals drift slowly, so it is cached far longer than
// the df/mount data (storageCacheTTL).
const (
	footprintFreshFor = 10 * time.Minute
	footprintMaxStale = 24 * time.Hour
)

// FootprintPolicy selects how a FootprintCache read trades age for latency.
type FootprintPolicy int

const (
	// FootprintFreshOnly serves an entry younger than FreshFor and otherwise
	// walks in the foreground. It is the zero value and the policy for
	// anything persisted (snapshots, baselines), whose growth math needs
	// the footprint to match the capture time.
	FootprintFreshOnly FootprintPolicy = iota
	// FootprintServeStale serves an entry younger than MaxStale, marked
	// stale when past FreshFor, and refreshes it with one background walk.
	// Only for long-lived processes; a one-shot CLI would exit mid-walk.
	FootprintServeStale
	// FootprintAcceptStale serves an entry younger than MaxStale, marked
	// stale when past FreshFor, without refreshing it; it walks only on a
	// full miss. For one-shot readers that report the age (spectra rules).
	FootprintAcceptStale
	// FootprintRefresh always walks and stores the result.
	FootprintRefresh
)

// FootprintCache serves the user footprint (library + caches bytes and the
// per-folder breakdown) on its own TTL, separate from the rest of
// storagestate.State. Each read names a FootprintPolicy, so one long-lived
// cache can serve stale data to interactive tools and fresh data to
// persisted snapshots. Walks are single-flight per key: concurrent callers
// share one walk.
type FootprintCache struct {
	// Store persists entries across processes; nil keeps them in memory.
	Store *cache.TTLStore
	// Clock judges entry age and stamps new entries; nil uses clock.Default.
	Clock clock.Clock
	// FreshFor and MaxStale default to 10 minutes and 24 hours.
	FreshFor time.Duration
	MaxStale time.Duration
	// Collect overrides the walk (tests). Nil uses storagestate.CollectFootprint.
	Collect func(osKind hostos.Kind, home string, now func() time.Time) storagestate.Footprint

	mu      sync.Mutex
	mem     map[string]storagestate.Footprint
	flights map[string]*footprintFlight
}

type footprintFlight struct {
	done chan struct{}
	fp   storagestate.Footprint
}

// NewFootprintCache returns a cache backed by store (may be nil).
func NewFootprintCache(store *cache.TTLStore) *FootprintCache {
	return &FootprintCache{Store: store}
}

// Footprint returns the user footprint for home under policy, and whether
// the returned value is past its freshness window.
func (c *FootprintCache) Footprint(osKind hostos.Kind, home string, policy FootprintPolicy) (storagestate.Footprint, bool) {
	osKind = hostos.Resolve(osKind)
	key := footprintCacheKey(osKind, home)
	if policy == FootprintRefresh {
		return c.wait(key, osKind, home, true), false
	}
	if fp, ok := c.lookup(key); ok {
		age := c.now().Sub(fp.AsOf)
		if age >= 0 && age < c.freshFor() {
			return fp, false
		}
		if age < c.maxStale() {
			switch policy {
			case FootprintServeStale:
				c.start(key, osKind, home, false)
				return fp, true
			case FootprintAcceptStale:
				return fp, true
			}
		}
	}
	return c.wait(key, osKind, home, false), false
}

// Refresh walks now (joining a walk already in flight) and stores the
// result, so later cached reads see it.
func (c *FootprintCache) Refresh(osKind hostos.Kind, home string) storagestate.Footprint {
	fp, _ := c.Footprint(osKind, home, FootprintRefresh)
	return fp
}

func (c *FootprintCache) wait(key string, osKind hostos.Kind, home string, force bool) storagestate.Footprint {
	f := c.start(key, osKind, home, force)
	<-f.done
	return f.fp
}

// lookup returns the newest entry between memory and the persistent store.
func (c *FootprintCache) lookup(key string) (storagestate.Footprint, bool) {
	c.mu.Lock()
	fp, ok := c.mem[key]
	c.mu.Unlock()
	if blob, hit := c.Store.Get([]byte(key)); hit {
		var stored storagestate.Footprint
		if err := json.Unmarshal(blob, &stored); err == nil && (!ok || stored.AsOf.After(fp.AsOf)) {
			fp, ok = stored, true
		}
	}
	return fp, ok
}

// start returns the in-flight walk for key, launching one if none runs.
// Unless force is set, a fresh in-memory result that another caller stored
// after this caller's lookup is returned as an already finished flight, so
// a racing caller never starts a second full walk.
func (c *FootprintCache) start(key string, osKind hostos.Kind, home string, force bool) *footprintFlight {
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.flights[key]; ok {
		return f
	}
	if !force {
		if fp, ok := c.mem[key]; ok {
			if age := c.now().Sub(fp.AsOf); age >= 0 && age < c.freshFor() {
				done := make(chan struct{})
				close(done)
				return &footprintFlight{done: done, fp: fp}
			}
		}
	}
	if c.flights == nil {
		c.flights = map[string]*footprintFlight{}
	}
	f := &footprintFlight{done: make(chan struct{})}
	c.flights[key] = f
	go c.run(key, osKind, home, f)
	return f
}

func (c *FootprintCache) run(key string, osKind hostos.Kind, home string, f *footprintFlight) {
	collect := c.Collect
	if collect == nil {
		collect = storagestate.CollectFootprint
	}
	f.fp = collect(osKind, home, c.now)
	c.mu.Lock()
	if c.mem == nil {
		c.mem = map[string]storagestate.Footprint{}
	}
	c.mem[key] = f.fp
	delete(c.flights, key)
	c.mu.Unlock()
	if c.Store != nil {
		if blob, err := json.Marshal(f.fp); err == nil {
			// Freshness is judged from AsOf; the store keeps the entry for
			// the whole stale window so stale-tolerant readers can use it.
			_ = c.Store.Put([]byte(key), blob, c.maxStale())
		}
	}
	close(f.done)
}

func (c *FootprintCache) now() time.Time {
	if c.Clock == nil {
		return clock.Default.Now()
	}
	return c.Clock.Now()
}

func (c *FootprintCache) freshFor() time.Duration {
	if c.FreshFor > 0 {
		return c.FreshFor
	}
	return footprintFreshFor
}

func (c *FootprintCache) maxStale() time.Duration {
	if c.MaxStale > 0 {
		return c.MaxStale
	}
	return footprintMaxStale
}

// footprintCacheKey keys on the resolved walk root, so a changed
// XDG_CACHE_HOME on Linux never reads another directory's entry. v2 entries
// carry the per-folder breakdown.
func footprintCacheKey(osKind hostos.Kind, home string) string {
	root := storagestate.FootprintRoot(osKind, home)
	h := sha256.Sum256([]byte(strings.Join([]string{"storage-footprint-v2", fmt.Sprint(int(osKind)), root}, "\x00")))
	return string(h[:])
}
