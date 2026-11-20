package cache

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// keyInShard returns a key whose hash lands in shard (two hex digits).
func keyInShard(t *testing.T, shard string, n int) []byte {
	t.Helper()
	for i := 0; i < 1<<20; i++ {
		k := Key([]byte(fmt.Sprintf("%d-%d", n, i)))
		if hex.EncodeToString(k)[:2] == shard {
			return k
		}
	}
	t.Fatalf("no key found for shard %s", shard)
	return nil
}

func TestPutPrunesAgedEntriesInItsShard(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := NewShardedStore(t.TempDir(), KindClassify).WithMaxAge(30 * 24 * time.Hour)
	s.SetClock(func() time.Time { return now })

	old, fresh, other := keyInShard(t, "ab", 1), keyInShard(t, "ab", 2), keyInShard(t, "cd", 3)
	for _, k := range [][]byte{old, fresh, other} {
		if err := s.Put(k, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	age := func(k []byte, d time.Duration) {
		if err := os.Chtimes(s.path(k), now.Add(-d), now.Add(-d)); err != nil {
			t.Fatal(err)
		}
	}
	age(old, 31*24*time.Hour)
	age(fresh, 29*24*time.Hour)
	age(other, 90*24*time.Hour)
	leftover := filepath.Join(s.root, "ab", ".tmp-crashed")
	if err := os.WriteFile(leftover, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(leftover, now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.Put(keyInShard(t, "ab", 4), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if s.Has(old) {
		t.Error("entry older than maxAge survived a Put to its shard")
	}
	if _, err := os.Stat(leftover); err == nil {
		t.Error("aged temp file survived a Put to its shard")
	}
	if !s.Has(fresh) {
		t.Error("entry younger than maxAge was pruned")
	}
	if !s.Has(other) {
		t.Error("a Put swept a shard it did not write to")
	}
}

func TestPutWithoutMaxAgeNeverPrunes(t *testing.T) {
	s := newTestStore(t, KindDetect)
	k := keyInShard(t, "ab", 1)
	if err := s.Put(k, []byte("x")); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-10 * 365 * 24 * time.Hour)
	if err := os.Chtimes(s.path(k), past, past); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(keyInShard(t, "ab", 2), []byte("y")); err != nil {
		t.Fatal(err)
	}
	if !s.Has(k) {
		t.Error("store without maxAge pruned an entry")
	}
}

func TestClassifyStorePrunes(t *testing.T) {
	stores := NewStores(t.TempDir(), &Registry{kinds: map[string]entry{}})
	if stores.Classify.maxAge != ClassifyMaxAge || stores.Detect.maxAge != 0 {
		t.Fatalf("classify maxAge=%v detect maxAge=%v", stores.Classify.maxAge, stores.Detect.maxAge)
	}
}
