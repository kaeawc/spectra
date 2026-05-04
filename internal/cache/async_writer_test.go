package cache

import (
	"bytes"
	"testing"
	"time"
)

func TestAsyncWriterBasic(t *testing.T) {
	s := newTestStore(t, "detect")
	w := NewAsyncWriter(s, 16, 2)

	key := Key([]byte("async-basic"))
	data := []byte("hello async")

	wroteSync := w.Write(key, data)
	w.Close() // drain before asserting

	// Regardless of sync/async, data must be in the store.
	got, ok, err := s.Get(key)
	if err != nil || !ok {
		t.Fatalf("data not in store after Write (sync=%v, err=%v)", wroteSync, err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestAsyncWriterMultiple(t *testing.T) {
	s := newTestStore(t, "detect")
	w := NewAsyncWriter(s, 32, 4)

	const n = 20
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = Key([]byte{byte(i), 0xAA})
		w.Write(keys[i], []byte("payload"))
	}
	w.Close()

	for i, k := range keys {
		_, ok, err := s.Get(k)
		if err != nil || !ok {
			t.Errorf("key[%d] missing after Close", i)
		}
	}
	if w.Completed.Load()+w.Failed.Load() != int64(n) {
		t.Errorf("completed+failed = %d, want %d", w.Completed.Load()+w.Failed.Load(), n)
	}
}

func TestAsyncWriterQueueFull(t *testing.T) {
	// Queue size 0 forces every write to fall back to synchronous.
	s := newTestStore(t, "detect")
	w := NewAsyncWriter(s, 0, 1)
	defer w.Close()

	key := Key([]byte("sync-fallback"))
	wroteSync := w.Write(key, []byte("x"))
	if !wroteSync {
		t.Error("expected synchronous fallback with queue size 0")
	}
	_, ok, _ := s.Get(key)
	if !ok {
		t.Error("data missing after synchronous fallback write")
	}
}

func TestAsyncWriterCounters(t *testing.T) {
	s := newTestStore(t, "detect")
	w := NewAsyncWriter(s, 64, 2)

	const n = 10
	for i := 0; i < n; i++ {
		w.Write(Key([]byte{byte(i)}), []byte("data"))
	}

	// Wait for drain without relying on Close timing.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.Queued.Load() > 0 || w.Completed.Load()+w.Failed.Load() >= n {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.Close()

	total := w.Completed.Load() + w.Failed.Load()
	if total != int64(n) {
		t.Errorf("completed+failed = %d, want %d", total, n)
	}
}
