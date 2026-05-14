package cache

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
	}
}
