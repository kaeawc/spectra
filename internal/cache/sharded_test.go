package cache

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T, kind string) *ShardedStore {
	t.Helper()
	return NewShardedStore(t.TempDir(), kind)
}

func TestPutGet(t *testing.T) {
	s := newTestStore(t, "detect")
	key := Key([]byte("test-app-v1"))
	data := []byte(`{"ui":"Electron"}`)

	if err := s.Put(key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatal("cache miss, want hit")
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Get = %q, want %q", got, data)
	}
}

func TestGetMiss(t *testing.T) {
	s := newTestStore(t, "detect")
	got, ok, err := s.Get(Key([]byte("not-there")))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("expected miss, got %q", got)
	}
}

func TestHas(t *testing.T) {
	s := newTestStore(t, "detect")
	key := Key([]byte("has-test"))
	if s.Has(key) {
		t.Error("Has: want false before Put")
	}
	_ = s.Put(key, []byte("x"))
	if !s.Has(key) {
		t.Error("Has: want true after Put")
	}
}

func TestPutIdempotent(t *testing.T) {
	s := newTestStore(t, "detect")
	key := Key([]byte("idempotent"))
	_ = s.Put(key, []byte("payload"))
	if err := s.Put(key, []byte("payload")); err != nil {
		t.Fatalf("second Put: %v", err)
	}
}

func TestStats(t *testing.T) {
	s := newTestStore(t, "detect")

	st, _ := s.Stats()
	if st.Entries != 0 {
		t.Errorf("empty store: Entries = %d, want 0", st.Entries)
	}

	for i := 0; i < 5; i++ {
		_ = s.Put(Key([]byte{byte(i)}), []byte("payload"))
	}
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Entries != 5 {
		t.Errorf("Entries = %d, want 5", st.Entries)
	}
	if st.BytesOnDisk == 0 {
		t.Error("BytesOnDisk should be > 0")
	}
	if st.LastWrite.IsZero() {
		t.Error("LastWrite should not be zero")
	}
}

func TestClear(t *testing.T) {
	s := newTestStore(t, "detect")
	for i := 0; i < 3; i++ {
		_ = s.Put(Key([]byte{byte(i)}), []byte("x"))
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	st, _ := s.Stats()
	if st.Entries != 0 {
		t.Errorf("after Clear, Entries = %d, want 0", st.Entries)
	}
}

func TestClearEmptyStore(t *testing.T) {
	s := newTestStore(t, "detect")
	if err := s.Clear(); err != nil {
		t.Errorf("Clear on non-existent store: %v", err)
	}
}

func TestTwoLevelSharding(t *testing.T) {
	root := t.TempDir()
	s := NewShardedStore(root, "detect")
	key := Key([]byte("sharding-test"))
	_ = s.Put(key, []byte("payload"))

	// Verify file lives at <root>/detect/<2 hex chars>/<remaining hex>
	h := sha256.Sum256([]byte("sharding-test"))
	hexStr := hex.EncodeToString(h[:])
	expected := filepath.Join(root, "detect", hexStr[:2], hexStr[2:])
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected shard file at %s: %v", expected, err)
	}
}

func TestKeyFunction(t *testing.T) {
	// Key(a+b) == Key(a, b) is NOT guaranteed — Key concatenates inputs.
	k1 := Key([]byte("hello world"))
	k2 := Key([]byte("hello"), []byte(" world"))
	// They feed the same bytes to SHA-256 so they should be equal.
	if !bytes.Equal(k1, k2) {
		t.Error("Key should produce same hash for same byte sequence")
	}
}
