// Package random provides a Random interface for sampling so production code
// can use crypto/rand while tests substitute a deterministic Seeded source.
//
// Inject Random anywhere you'd otherwise reach for math/rand or crypto/rand —
// pick a random element, shuffle a slice, generate a UUID — and tests can
// assert on exact outputs by passing a Seeded with a known seed.
package random

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
)

// Random is the source-of-randomness interface.
type Random interface {
	// Float64 returns a float in [0.0, 1.0).
	Float64() float64
	// IntN returns an int in [0, n). Panics if n <= 0.
	IntN(n int) int
	// IntRange returns an int in [lo, hi] (both inclusive).
	IntRange(lo, hi int) int
	// Bytes returns n cryptographically-flavored bytes (or pseudo-random
	// bytes from a Seeded source). Returns an error if the underlying source
	// fails (only Crypto can fail).
	Bytes(n int) ([]byte, error)
	// UUID returns an RFC-4122 v4 UUID string.
	UUID() string
}

// Crypto reads from crypto/rand. Use this in production.
type Crypto struct{}

// NewCrypto returns a Crypto Random.
func NewCrypto() *Crypto { return &Crypto{} }

func (c *Crypto) Float64() float64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	// Use top 53 bits to avoid bias when converting to float64.
	u := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(u) / (1 << 53)
}

func (c *Crypto) IntN(n int) int {
	if n <= 0 {
		panic("random: IntN requires n > 0")
	}
	return int(c.Float64() * float64(n))
}

func (c *Crypto) IntRange(lo, hi int) int {
	if hi < lo {
		panic("random: IntRange requires hi >= lo")
	}
	return lo + c.IntN(hi-lo+1)
}

func (c *Crypto) Bytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, errors.New("random: Bytes requires n >= 0")
	}
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		return nil, fmt.Errorf("crypto/rand: %w", err)
	}
	return b, nil
}

func (c *Crypto) UUID() string {
	var b [16]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return formatUUID(b)
}

// Seeded is a deterministic Random backed by math/rand/v2 with a fixed seed.
// Use this in tests so output is reproducible across runs.
type Seeded struct {
	r *rand.Rand
}

// NewSeeded returns a Seeded source initialized with seed. Seeded uses
// math/rand/v2 (PCG) — fast and reproducible for tests, NOT cryptographically
// secure. Production code should use Crypto.
func NewSeeded(seed uint64) *Seeded {
	// #nosec G404 -- intentional non-crypto PRNG for deterministic test fixtures
	return &Seeded{r: rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))}
}

func (s *Seeded) Float64() float64 { return s.r.Float64() }

func (s *Seeded) IntN(n int) int {
	if n <= 0 {
		panic("random: IntN requires n > 0")
	}
	return s.r.IntN(n)
}

func (s *Seeded) IntRange(lo, hi int) int {
	if hi < lo {
		panic("random: IntRange requires hi >= lo")
	}
	return lo + s.r.IntN(hi-lo+1)
}

func (s *Seeded) Bytes(n int) ([]byte, error) {
	if n < 0 {
		return nil, errors.New("random: Bytes requires n >= 0")
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(s.r.UintN(256)) // #nosec G115 -- 0..255 fits in byte by construction
	}
	return b, nil
}

func (s *Seeded) UUID() string {
	var b [16]byte
	for i := range b {
		b[i] = byte(s.r.UintN(256)) // #nosec G115 -- 0..255 fits in byte by construction
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatUUID(b)
}

func formatUUID(b [16]byte) string {
	const hex = "0123456789abcdef"
	var out [36]byte
	j := 0
	for i, x := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out[j] = '-'
			j++
		}
		out[j] = hex[x>>4]
		out[j+1] = hex[x&0x0f]
		j += 2
	}
	return string(out[:])
}

// Pick returns a random element from items. Panics if items is empty.
func Pick[T any](r Random, items []T) T {
	if len(items) == 0 {
		panic("random: Pick on empty slice")
	}
	return items[r.IntN(len(items))]
}

// Shuffle returns a Fisher-Yates shuffled copy of items.
func Shuffle[T any](r Random, items []T) []T {
	out := make([]T, len(items))
	copy(out, items)
	for i := len(out) - 1; i > 0; i-- {
		j := r.IntN(i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}
