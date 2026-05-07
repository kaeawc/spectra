package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/kaeawc/spectra/internal/fsutil"
)

// JSONStore persists the manifest as a single atomically-rewritten JSON file.
type JSONStore struct {
	path string
	mu   sync.Mutex
}

// NewJSONStore returns a JSON-backed Store at path.
func NewJSONStore(path string) *JSONStore {
	return &JSONStore{path: path}
}

// Append adds rec to the manifest.
func (s *JSONStore) Append(ctx context.Context, rec Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.readLocked()
	if err != nil {
		return err
	}
	records = append(records, rec)
	return s.writeLocked(records)
}

// List returns the manifest records in insertion order.
func (s *JSONStore) List(ctx context.Context) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked()
}

func (s *JSONStore) readLocked() ([]Record, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("artifact manifest read: %w", err)
	}
	var records []Record
	if len(data) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("artifact manifest decode: %w", err)
	}
	return records, nil
}

func (s *JSONStore) writeLocked(records []Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("artifact manifest mkdir: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("artifact manifest encode: %w", err)
	}
	data = append(data, '\n')
	if err := fsutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("artifact manifest write: %w", err)
	}
	return nil
}
