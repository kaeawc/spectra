package cache

import "time"

// Well-known cache kind names. Each must match the subdirectory name under
// the versioned root. Using constants avoids typos across callers.
const (
	KindDetect    = "detect"
	KindHprof     = "hprof"
	KindJFR       = "jfr"
	KindThreads   = "threads"
	KindSamples   = "samples"
	KindNetcap    = "netcap"
	KindToolchain = "toolchain"
	KindStorage   = "storage"
	KindClassify  = "classify"
)

// Stores holds the concrete ShardedStore instances for every kind.
// Callers that need to read or write a specific kind use these directly;
// the Registry is only for CLI-level stats/clear.
type Stores struct {
	Detect    *ShardedStore
	Hprof     *ShardedStore
	JFR       *ShardedStore
	Threads   *ShardedStore
	Samples   *ShardedStore
	Netcap    *ShardedStore
	Toolchain *ShardedStore
	Storage   *ShardedStore
	Classify  *ShardedStore
}

// NewStores creates all ShardedStores under versionedRoot and registers
// them in the provided Registry.
func NewStores(versionedRoot string, reg *Registry) *Stores {
	mk := func(kind string) *ShardedStore {
		s := NewShardedStore(versionedRoot, kind)
		reg.RegisterStore(s)
		return s
	}
	return &Stores{
		Detect:    mk(KindDetect),
		Hprof:     mk(KindHprof),
		JFR:       mk(KindJFR),
		Threads:   mk(KindThreads),
		Samples:   mk(KindSamples),
		Netcap:    mk(KindNetcap),
		Toolchain: mk(KindToolchain),
		Storage:   mk(KindStorage),
		Classify:  mk(KindClassify).WithMaxAge(ClassifyMaxAge),
	}
}

// ClassifyMaxAge is how long a classify entry survives after it was written.
// Entries are keyed by executable identity, so every rebuilt or updated
// binary orphans its old entry; pruning on write bounds the store, and an
// entry for an unchanged binary costs one recomputation per period.
const ClassifyMaxAge = 30 * 24 * time.Hour
