// Package cache implements the two-level hash-sharded blob store described in
// docs/design/storage.md (Tier 2) and docs/operations/caching.md.
//
// Layout:
//
//	~/.cache/spectra/v1/<kind>/<hash[:2]>/<hash[2:]>
//
// Two-level sharding keeps no directory above 256 entries at scale.
// Writes are atomic via tempfile-rename. Read/write are goroutine-safe.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ShardedStore is a content-addressed file store for one cache kind.
// Multiple ShardedStores share one versioned root (e.g. ~/.cache/spectra/v1).
type ShardedStore struct {
	root string // absolute path to <versionedRoot>/<kind>

	// maxAge, when positive, makes every Put sweep the shard it wrote to,
	// removing entries last written more than maxAge ago.
	maxAge time.Duration
	now    func() time.Time
}

// pruneBatch bounds how many directory entries one Put's sweep examines.
const pruneBatch = 256

// WithMaxAge turns on age-based pruning and returns s. Each Put then sweeps
// the one shard directory it wrote to (at most pruneBatch entries) and
// removes files whose mtime is older than maxAge, including temp files a
// crashed writer left. Keys hash uniformly across the 256 shards, so the
// store stays bounded by what was written in the last maxAge with no
// separate GC pass. An entry is not refreshed on read: one in steady use is
// rewritten after it ages out and its next lookup misses.
func (s *ShardedStore) WithMaxAge(maxAge time.Duration) *ShardedStore {
	s.maxAge = maxAge
	return s
}

// SetClock overrides the time source pruning compares mtimes against.
func (s *ShardedStore) SetClock(clk func() time.Time) { s.now = clk }

// NewShardedStore returns a store rooted at <versionedRoot>/<kind>.
// The directory is created on first Put, not here.
func NewShardedStore(versionedRoot, kind string) *ShardedStore {
	return &ShardedStore{root: filepath.Join(versionedRoot, kind)}
}

// Key hashes arbitrary bytes to a hex string used as the file path.
func Key(b ...[]byte) []byte {
	h := sha256.New()
	for _, chunk := range b {
		h.Write(chunk)
	}
	return h.Sum(nil)
}

// path returns the absolute path for a given raw key.
func (s *ShardedStore) path(key []byte) string {
	hex := hex.EncodeToString(key)
	return filepath.Join(s.root, hex[:2], hex[2:])
}

// Put writes data to the store under key. The write is atomic:
// data is written to a temp file in the same directory, then renamed.
func (s *ShardedStore) Put(key, data []byte) error {
	dest := s.path(key)
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cache: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("cache: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("cache: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		return err
	}
	if s.maxAge > 0 {
		s.prune(dir, filepath.Base(dest))
	}
	return nil
}

// prune removes files in one shard directory older than maxAge, skipping
// keep (the entry just written). It is best-effort: a failure leaves the
// file for a later sweep.
func (s *ShardedStore) prune(dir, keep string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	entries, _ := d.ReadDir(pruneBatch)
	_ = d.Close()
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	cutoff := now().Add(-s.maxAge)
	for _, e := range entries {
		if e.IsDir() || e.Name() == keep {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// Get retrieves data for key. Returns (nil, false, nil) on cache miss.
func (s *ShardedStore) Get(key []byte) ([]byte, bool, error) {
	data, err := os.ReadFile(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Has reports whether a key exists without reading the full file.
func (s *ShardedStore) Has(key []byte) bool {
	_, err := os.Stat(s.path(key))
	return err == nil
}

// Stats returns aggregate statistics for this store kind.
func (s *ShardedStore) Stats() (StoreStats, error) {
	var st StoreStats
	st.Kind = filepath.Base(s.root)
	err := filepath.WalkDir(s.root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		st.Entries++
		st.BytesOnDisk += info.Size()
		if info.ModTime().After(st.LastWrite) {
			st.LastWrite = info.ModTime()
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	return st, err
}

// Clear removes all files in this store. The root directory itself is kept.
func (s *ShardedStore) Clear() error {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.root, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// StoreStats is the aggregate stats for one cache kind.
type StoreStats struct {
	Kind        string
	Entries     int64
	BytesOnDisk int64
	LastWrite   time.Time
}

// DefaultRoot returns the default versioned root: ~/.cache/spectra/v1.
func DefaultRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "spectra", "v1"), nil
}

// ReadAt returns an io.ReadCloser for the file at path. Useful for large blobs.
func (s *ShardedStore) ReadAt(key []byte) (io.ReadCloser, error) {
	f, err := os.Open(s.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return f, err
}
