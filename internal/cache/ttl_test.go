package cache

import (
	"testing"
	"time"
)

func TestTTLStore_HitWithinTTL(t *testing.T) {
	s := NewShardedStore(t.TempDir(), "ttl-test")
	ts := NewTTLStore(s, nil)
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	ts.SetClock(func() time.Time { return now })

	key := Key([]byte("k"))
	if err := ts.Put(key, []byte("hello"), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok := ts.Get(key)
	if !ok || string(got) != "hello" {
		t.Errorf("Get within TTL: got=%q ok=%v want hello/true", got, ok)
	}
}

func TestTTLStore_MissAfterTTL(t *testing.T) {
	s := NewShardedStore(t.TempDir(), "ttl-test")
	ts := NewTTLStore(s, nil)
	t0 := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	clock := t0
	ts.SetClock(func() time.Time { return clock })

	key := Key([]byte("k"))
	_ = ts.Put(key, []byte("hello"), 30*time.Second)

	clock = t0.Add(31 * time.Second)
	if _, ok := ts.Get(key); ok {
		t.Error("expected miss after TTL elapsed")
	}
}

func TestTTLStore_MissOnUnknownKey(t *testing.T) {
	ts := NewTTLStore(NewShardedStore(t.TempDir(), "ttl-test"), nil)
	if _, ok := ts.Get(Key([]byte("nonexistent"))); ok {
		t.Error("expected miss for unknown key")
	}
}

func TestTTLStore_AsyncWriterPath(t *testing.T) {
	s := NewShardedStore(t.TempDir(), "ttl-test")
	w := NewAsyncWriter(s, 4, 1)
	defer w.Close()

	ts := NewTTLStore(s, w)
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	ts.SetClock(func() time.Time { return now })

	key := Key([]byte("async"))
	if err := ts.Put(key, []byte("payload"), time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Drain the queue.
	w.Close()
	got, ok := ts.Get(key)
	if !ok || string(got) != "payload" {
		t.Errorf("async path: got=%q ok=%v want payload/true", got, ok)
	}
}

func TestTTLStore_NilSafeGet(t *testing.T) {
	var ts *TTLStore
	if _, ok := ts.Get(Key([]byte("k"))); ok {
		t.Error("nil receiver Get should miss")
	}
}
