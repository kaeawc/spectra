package livehistory

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestRingRecentKeepsChronologicalOrder(t *testing.T) {
	r := NewRing(3)
	base := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		r.Add(snapshot.Snapshot{
			ID:      string(rune('a' + i)),
			TakenAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

	got := r.Recent(0)
	if len(got) != 3 {
		t.Fatalf("Recent len = %d, want 3", len(got))
	}
	for i, want := range []string{"c", "d", "e"} {
		if got[i].ID != want {
			t.Errorf("Recent[%d].ID = %q, want %q", i, got[i].ID, want)
		}
	}
}

func TestRingLimitAndLatest(t *testing.T) {
	r := NewRing(4)
	r.Add(snapshot.Snapshot{ID: "first"})
	r.Add(snapshot.Snapshot{ID: "second"})

	recent := r.Recent(1)
	if len(recent) != 1 || recent[0].ID != "second" {
		t.Fatalf("Recent(1) = %+v, want second", recent)
	}

	latest, ok := r.Latest()
	if !ok {
		t.Fatal("Latest ok = false, want true")
	}
	if latest.ID != "second" {
		t.Errorf("Latest ID = %q, want second", latest.ID)
	}
}
