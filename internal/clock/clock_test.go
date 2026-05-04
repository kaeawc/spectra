package clock

import (
	"sync"
	"testing"
	"time"
)

func TestSystem_Now_IsMonotonicAndRecent(t *testing.T) {
	t.Parallel()

	before := time.Now()
	got := System{}.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Fatalf("System.Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestDefault_IsSystem(t *testing.T) {
	t.Parallel()

	if _, ok := Default.(System); !ok {
		t.Fatalf("Default = %T, want System", Default)
	}
}

func TestFake_NowReportsConfiguredTime(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	f := NewFake(start)

	if got := f.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}
}

func TestFake_ZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	var f Fake
	if got := f.Now(); !got.IsZero() {
		t.Fatalf("zero-value Fake Now() = %v, want zero time", got)
	}
	f.Advance(time.Hour)
	if got := f.Now(); got.Sub(time.Time{}) != time.Hour {
		t.Fatalf("after Advance(1h), Now() = %v, want zero+1h", got)
	}
}

func TestFake_Set(t *testing.T) {
	t.Parallel()

	f := NewFake(time.Unix(0, 0))
	target := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	f.Set(target)

	if got := f.Now(); !got.Equal(target) {
		t.Fatalf("Now() after Set = %v, want %v", got, target)
	}
}

func TestFake_AdvanceForwardAndBackward(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	f := NewFake(start)

	f.Advance(2 * time.Hour)
	if got := f.Now(); !got.Equal(start.Add(2 * time.Hour)) {
		t.Fatalf("Now() after Advance(+2h) = %v, want %v", got, start.Add(2*time.Hour))
	}

	f.Advance(-30 * time.Minute)
	want := start.Add(2*time.Hour - 30*time.Minute)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("Now() after Advance(-30m) = %v, want %v", got, want)
	}
}

func TestFake_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	f := NewFake(time.Unix(0, 0))
	const goroutines = 16
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				f.Advance(time.Nanosecond)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = f.Now()
			}
		}()
	}
	wg.Wait()

	want := time.Unix(0, 0).Add(time.Duration(goroutines*iterations) * time.Nanosecond)
	if got := f.Now(); !got.Equal(want) {
		t.Fatalf("after concurrent advances, Now() = %v, want %v", got, want)
	}
}

// Compile-time assertion that *Fake and System both satisfy Clock.
var (
	_ Clock = (*Fake)(nil)
	_ Clock = System{}
)
