package cache

import (
	"encoding/binary"
	"errors"
	"time"
)

// TTLStore wraps a ShardedStore with a time-based freshness check, so
// non-content-addressed cache entries (whole-collector outputs that
// snapshot a moment of host state) can be reused for a bounded window.
//
// Layout: the on-disk blob is "<8-byte big-endian unix-nano expires_at><payload>"
// so a Get can decide freshness without unmarshaling the payload.
//
// Writes go through an AsyncWriter; if the queue is full the writer falls
// back to a synchronous Put internally, so callers never block on cache I/O.
type TTLStore struct {
	store  *ShardedStore
	writer *AsyncWriter
	clock  func() time.Time
}

// NewTTLStore returns a TTL wrapper. writer may be nil for synchronous-only
// writes; pass NewAsyncWriter(store, ...) to make writes non-blocking.
func NewTTLStore(store *ShardedStore, writer *AsyncWriter) *TTLStore {
	return &TTLStore{store: store, writer: writer, clock: time.Now}
}

// SetClock overrides the time source for tests.
func (t *TTLStore) SetClock(clk func() time.Time) { t.clock = clk }

// Get returns the cached payload if it exists and has not expired. ok=false
// covers all "no usable cache" cases: missing, malformed, expired.
func (t *TTLStore) Get(key []byte) (payload []byte, ok bool) {
	if t == nil || t.store == nil {
		return nil, false
	}
	blob, present, err := t.store.Get(key)
	if err != nil || !present || len(blob) < 8 {
		return nil, false
	}
	expiresAt := time.Unix(0, int64(binary.BigEndian.Uint64(blob[:8])))
	if !t.clock().Before(expiresAt) {
		return nil, false
	}
	out := make([]byte, len(blob)-8)
	copy(out, blob[8:])
	return out, true
}

// Put stores payload with a freshness window of ttl. Errors from a sync
// fallback are returned; async writes are fire-and-forget per AsyncWriter.
func (t *TTLStore) Put(key, payload []byte, ttl time.Duration) error {
	if t == nil || t.store == nil {
		return errors.New("ttl store: nil backing store")
	}
	expiresAt := t.clock().Add(ttl).UnixNano()
	blob := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint64(blob[:8], uint64(expiresAt))
	copy(blob[8:], payload)
	if t.writer != nil {
		t.writer.Write(key, blob)
		return nil
	}
	return t.store.Put(key, blob)
}
