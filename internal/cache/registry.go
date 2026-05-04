package cache

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
)

// Registry tracks all registered cache kinds. Each kind registers itself
// with a Clear and Stats function; the CLI then exposes uniform
// `spectra cache stats` and `spectra cache clear` without per-kind code.
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]entry
}

type entry struct {
	clear func() error
	stats func() (StoreStats, error)
}

// Default is the package-level registry all stores register into.
var Default = &Registry{kinds: map[string]entry{}}

// Register adds a cache kind to the registry. Duplicate names overwrite.
func (r *Registry) Register(name string, clearFn func() error, statsFn func() (StoreStats, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kinds[name] = entry{clear: clearFn, stats: statsFn}
}

// RegisterStore is a convenience helper that registers a ShardedStore
// under its directory name.
func (r *Registry) RegisterStore(s *ShardedStore) {
	name := filepath.Base(s.root)
	r.Register(name, s.Clear, s.Stats)
}

// Stats returns stats for all registered kinds, sorted by name.
func (r *Registry) Stats() ([]StoreStats, error) {
	r.mu.RLock()
	names := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		names = append(names, k)
	}
	r.mu.RUnlock()

	sort.Strings(names)
	out := make([]StoreStats, 0, len(names))
	for _, name := range names {
		r.mu.RLock()
		e := r.kinds[name]
		r.mu.RUnlock()

		st, err := e.stats()
		if err != nil {
			return nil, fmt.Errorf("cache stats %q: %w", name, err)
		}
		out = append(out, st)
	}
	return out, nil
}

// Clear removes all data for the named kind (or every kind when name=="").
func (r *Registry) Clear(name string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if name != "" {
		e, ok := r.kinds[name]
		if !ok {
			return fmt.Errorf("cache: unknown kind %q", name)
		}
		return e.clear()
	}
	for _, e := range r.kinds {
		if err := e.clear(); err != nil {
			return err
		}
	}
	return nil
}

// Names returns registered kind names sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.kinds))
	for k := range r.kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
