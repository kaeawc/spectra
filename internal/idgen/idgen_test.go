package idgen

import (
	"regexp"
	"sync"
	"testing"
)

func TestUUID_NextIsHexAndUnique(t *testing.T) {
	t.Parallel()

	hex := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := make(map[string]bool)
	const n = 1000
	g := UUID{}
	for i := 0; i < n; i++ {
		id := g.Next()
		if !hex.MatchString(id) {
			t.Fatalf("id %q does not match 32-char lowercase hex", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q at iteration %d", id, i)
		}
		seen[id] = true
	}
}

func TestDefault_IsUUID(t *testing.T) {
	t.Parallel()
	if _, ok := Default.(UUID); !ok {
		t.Fatalf("Default = %T, want UUID", Default)
	}
}

func TestSequence_PrefixesAndIncrements(t *testing.T) {
	t.Parallel()
	s := NewSequence("req")
	want := []string{"req-1", "req-2", "req-3"}
	for i, w := range want {
		if got := s.Next(); got != w {
			t.Errorf("Next #%d = %q, want %q", i, got, w)
		}
	}
}

func TestSequence_DefaultPrefix(t *testing.T) {
	t.Parallel()
	s := NewSequence("")
	if got := s.Next(); got != "id-1" {
		t.Fatalf("got %q, want id-1", got)
	}
}

func TestSequence_Reset(t *testing.T) {
	t.Parallel()
	s := NewSequence("x")
	_ = s.Next()
	_ = s.Next()
	s.Reset()
	if got := s.Next(); got != "x-1" {
		t.Fatalf("got %q after Reset, want x-1", got)
	}
}

func TestSequence_ConcurrentNextProducesUniqueIds(t *testing.T) {
	t.Parallel()
	s := NewSequence("c")
	const goroutines = 16
	const iterations = 100

	out := make(chan string, goroutines*iterations)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				out <- s.Next()
			}
		}()
	}
	wg.Wait()
	close(out)

	seen := make(map[string]bool)
	for id := range out {
		if seen[id] {
			t.Fatalf("duplicate id %q under concurrent Next", id)
		}
		seen[id] = true
	}
	if len(seen) != goroutines*iterations {
		t.Fatalf("got %d unique ids, want %d", len(seen), goroutines*iterations)
	}
}

// Compile-time assertions.
var (
	_ Generator = UUID{}
	_ Generator = (*Sequence)(nil)
)
