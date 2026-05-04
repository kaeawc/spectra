package random

import (
	"regexp"
	"testing"
)

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestCryptoUUIDFormat(t *testing.T) {
	c := NewCrypto()
	for i := 0; i < 100; i++ {
		u := c.UUID()
		if !uuidRE.MatchString(u) {
			t.Fatalf("invalid UUID: %q", u)
		}
	}
}

func TestCryptoUUIDsAreUnique(t *testing.T) {
	c := NewCrypto()
	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		u := c.UUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("duplicate UUID after %d iterations: %s", i, u)
		}
		seen[u] = struct{}{}
	}
}

func TestSeededIsDeterministic(t *testing.T) {
	a := NewSeeded(42)
	b := NewSeeded(42)
	for i := 0; i < 50; i++ {
		if a.IntN(1000) != b.IntN(1000) {
			t.Fatalf("seeded sources diverged at iteration %d", i)
		}
	}
}

func TestSeededDifferentSeedsDiffer(t *testing.T) {
	a := NewSeeded(1)
	b := NewSeeded(2)
	same := 0
	for i := 0; i < 20; i++ {
		if a.IntN(1<<30) == b.IntN(1<<30) {
			same++
		}
	}
	if same > 2 {
		t.Errorf("seeds 1 and 2 produced %d matching values out of 20", same)
	}
}

func TestSeededUUIDFormat(t *testing.T) {
	s := NewSeeded(7)
	for i := 0; i < 20; i++ {
		u := s.UUID()
		if !uuidRE.MatchString(u) {
			t.Fatalf("invalid UUID: %q", u)
		}
	}
}

func TestIntRange(t *testing.T) {
	s := NewSeeded(99)
	for i := 0; i < 1000; i++ {
		v := s.IntRange(5, 10)
		if v < 5 || v > 10 {
			t.Fatalf("IntRange(5,10) = %d out of bounds", v)
		}
	}
}

func TestIntNPanicsOnZero(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on IntN(0)")
		}
	}()
	NewSeeded(1).IntN(0)
}

func TestPickAndShuffle(t *testing.T) {
	s := NewSeeded(123)
	items := []int{1, 2, 3, 4, 5}
	v := Pick(s, items)
	if v < 1 || v > 5 {
		t.Errorf("Pick returned %d", v)
	}

	shuffled := Shuffle(s, items)
	if len(shuffled) != len(items) {
		t.Fatalf("Shuffle changed length: %d", len(shuffled))
	}
	sum1, sum2 := 0, 0
	for i := range items {
		sum1 += items[i]
		sum2 += shuffled[i]
	}
	if sum1 != sum2 {
		t.Errorf("Shuffle changed elements: %v vs %v", items, shuffled)
	}
	if &items[0] == &shuffled[0] {
		t.Error("Shuffle should not alias the input slice")
	}
}

func TestBytesLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 1024} {
		b, err := NewCrypto().Bytes(n)
		if err != nil {
			t.Fatalf("Bytes(%d): %v", n, err)
		}
		if len(b) != n {
			t.Errorf("Bytes(%d) returned %d bytes", n, len(b))
		}
	}
}
