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
}

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
	return os.Rename(tmpPath, dest)
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
