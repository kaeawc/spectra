// Package idgen provides a small abstraction for generating opaque
// string ids. Production code uses UUID; tests substitute Sequence
// to make ids deterministic without seeding crypto/rand.
//
// Krit's existing id-shaped values (cache shard ids, oracle fingerprints,
// experiment names) are derived from content hashes and remain
// deterministic without this package — idgen is for the cases where
// a request id, span id, or scratch identifier needs to be generated
// fresh and a test wants to assert the exact string it sees.
//
// A separate Random abstraction is intentionally not provided: the
// only call sites in the Krit tree that use math/rand are sampling
// helpers that already seed deterministically from input, so a fake
// would not improve any current test.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// Generator produces successive ids. Implementations must be safe
// for concurrent use.
type Generator interface {
	// Next returns the next id. Implementations should never return
	// the empty string.
	Next() string
}

// UUID generates random 16-byte hex ids using crypto/rand. Each call
// produces 32 lowercase hex characters with no dashes.
type UUID struct{}

// Next returns a fresh hex-encoded random id.
func (UUID) Next() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand.Read on POSIX is documented to never fail in
		// practice. Wrap any platform exception so callers get a
		// readable trace if it ever does.
		panic(fmt.Errorf("idgen: crypto/rand.Read failed: %w", err))
	}
	return hex.EncodeToString(buf[:])
}

// Default is a process-wide UUID generator for callers that cannot
// reach a composition root. Prefer injecting a Generator explicitly.
var Default Generator = UUID{}

// Sequence emits prefixed counter ids: "id-1", "id-2", ... A custom
// prefix may be supplied. Sequence is safe for concurrent use.
type Sequence struct {
	mu     sync.Mutex
	prefix string
	n      uint64
}

// NewSequence returns a Sequence using the given prefix. An empty
// prefix is replaced with "id".
func NewSequence(prefix string) *Sequence {
	if prefix == "" {
		prefix = "id"
	}
	return &Sequence{prefix: prefix}
}

// Next returns the next prefixed counter id.
func (s *Sequence) Next() string {
	s.mu.Lock()
	s.n++
	n := s.n
	s.mu.Unlock()
	return fmt.Sprintf("%s-%d", s.prefix, n)
}

// Reset returns the sequence's internal counter to zero. Subsequent
// Next calls start at 1 again.
func (s *Sequence) Reset() {
	s.mu.Lock()
	s.n = 0
	s.mu.Unlock()
}
