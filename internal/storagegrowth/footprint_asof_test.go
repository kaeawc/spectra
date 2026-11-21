package storagegrowth

import (
	"testing"
	"time"
)

// A cached footprint can predate its snapshot; the footprint's own
// measurement times give the rate, not the snapshot interval.
func TestComputeFootprintRateUsesAsOf(t *testing.T) {
	b := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := b.Add(2 * 24 * time.Hour)
	before := state(nil, 1000, 100, nil)
	after := state(nil, 5000, 500, nil)
	before.UserFootprintAsOf = b.Add(-2 * 24 * time.Hour) // 4 days between measurements
	after.UserFootprintAsOf = a

	rep := Compute(before, after, b, a, 0)
	lib, ok := deltaFor(rep, "user-library", "~/Library")
	if !ok || lib.BytesPerDay != 1000 {
		t.Errorf("library rate = %v (found %v), want 1000/day over 4 days", lib.BytesPerDay, ok)
	}
	caches, _ := deltaFor(rep, "app-caches", "~/Library/Caches")
	if caches.BytesPerDay != 100 {
		t.Errorf("caches rate = %v, want 100/day", caches.BytesPerDay)
	}
	if rep.Days != 2 {
		t.Errorf("report interval = %v, want snapshot interval 2", rep.Days)
	}
}

func TestComputeFootprintRateFallsBackWithoutAsOf(t *testing.T) {
	b := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep := Compute(state(nil, 1000, 0, nil), state(nil, 3000, 0, nil), b, b.Add(2*24*time.Hour), 0)
	if lib, _ := deltaFor(rep, "user-library", "~/Library"); lib.BytesPerDay != 1000 {
		t.Errorf("library rate = %v, want 1000/day", lib.BytesPerDay)
	}
}
