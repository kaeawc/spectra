package cache

import (
	"errors"
	"testing"
)

func newTestRegistry() *Registry {
	return &Registry{kinds: map[string]entry{}}
}

func TestRegistryRegisterAndStats(t *testing.T) {
	reg := newTestRegistry()
	s := newTestStore(t, "detect")
	reg.RegisterStore(s)

	_ = s.Put(Key([]byte("x")), []byte("data"))

	stats, err := reg.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("got %d stat rows, want 1", len(stats))
	}
	if stats[0].Entries != 1 {
		t.Errorf("Entries = %d, want 1", stats[0].Entries)
	}
}

func TestRegistryClearByName(t *testing.T) {
	reg := newTestRegistry()
	s := newTestStore(t, "detect")
	reg.RegisterStore(s)
	_ = s.Put(Key([]byte("y")), []byte("data"))

	if err := reg.Clear("detect"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.Stats()
	if st.Entries != 0 {
		t.Errorf("after Clear(detect), Entries = %d", st.Entries)
	}
}

func TestRegistryClearAll(t *testing.T) {
	reg := newTestRegistry()
	s1 := newTestStore(t, "detect")
	s2 := newTestStore(t, "hprof")
	reg.RegisterStore(s1)
	reg.RegisterStore(s2)
	_ = s1.Put(Key([]byte("1")), []byte("x"))
	_ = s2.Put(Key([]byte("2")), []byte("y"))

	if err := reg.Clear(""); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*ShardedStore{s1, s2} {
		st, _ := s.Stats()
		if st.Entries != 0 {
			t.Errorf("Entries = %d after clear-all", st.Entries)
		}
	}
}

func TestRegistryClearUnknown(t *testing.T) {
	reg := newTestRegistry()
	err := reg.Clear("no-such-kind")
	if err == nil {
		t.Error("expected error clearing unknown kind")
	}
}

func TestRegistryNames(t *testing.T) {
	reg := newTestRegistry()
	reg.Register("bravo", func() error { return nil }, func() (StoreStats, error) { return StoreStats{}, nil })
	reg.Register("alpha", func() error { return nil }, func() (StoreStats, error) { return StoreStats{}, nil })
	names := reg.Names()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "bravo" {
		t.Errorf("Names = %v, want [alpha bravo]", names)
	}
}

func TestRegistryStatsPropagatesError(t *testing.T) {
	reg := newTestRegistry()
	reg.Register("bad", func() error { return nil }, func() (StoreStats, error) {
		return StoreStats{}, errors.New("stats failed")
	})
	_, err := reg.Stats()
	if err == nil {
		t.Error("expected error from Stats when a kind errors")
	}
}
