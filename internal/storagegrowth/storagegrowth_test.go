package storagegrowth

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/storagestate"
)

func state(vols map[string]int64, lib, caches int64, apps map[string]int64) storagestate.State {
	s := storagestate.State{UserLibraryBytes: lib, AppCachesBytes: caches}
	for mp, used := range vols {
		s.Volumes = append(s.Volumes, storagestate.Volume{MountPoint: mp, UsedBytes: used})
	}
	for p, b := range apps {
		s.LargestApps = append(s.LargestApps, storagestate.AppSize{Path: p, OnDiskBytes: b})
	}
	return s
}

func deltaFor(rep Report, cat, label string) (Delta, bool) {
	for _, d := range rep.Deltas {
		if d.Category == cat && d.Label == label {
			return d, true
		}
	}
	return Delta{}, false
}

func TestComputeRanksGrowthAndRate(t *testing.T) {
	before := state(map[string]int64{"/": 1000}, 500, 200, map[string]int64{"/Applications/Foo.app": 100})
	// caches +800 (biggest), volume +600, new app Bar +300, library +20; Foo unchanged.
	after := state(map[string]int64{"/": 1600}, 520, 1000, map[string]int64{
		"/Applications/Foo.app": 100, "/Applications/Bar.app": 300,
	})
	b := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := b.Add(2 * 24 * time.Hour)

	rep := Compute(before, after, b, a, 0)
	if rep.Days != 2 {
		t.Errorf("days = %v, want 2", rep.Days)
	}
	// Ranked by growth desc: app-caches(800) first.
	if rep.Deltas[0].Category != "app-caches" || rep.Deltas[0].GrowthBytes != 800 {
		t.Errorf("top grower = %+v, want app-caches +800", rep.Deltas[0])
	}
	// Rate = growth / days: 800 / 2 = 400/day.
	if rep.Deltas[0].BytesPerDay != 400 {
		t.Errorf("rate = %v, want 400/day", rep.Deltas[0].BytesPerDay)
	}
	// New app counted from zero.
	if d, ok := deltaFor(rep, "app", "/Applications/Bar.app"); !ok || d.BeforeBytes != 0 || d.GrowthBytes != 300 {
		t.Errorf("new app delta = %+v (ok=%v)", d, ok)
	}
	// Unchanged app must not appear among growers.
	if _, ok := deltaFor(rep, "app", "/Applications/Foo.app"); ok {
		t.Error("unchanged Foo.app should not be a grower")
	}
}

func TestComputeExcludesShrinkersFromRankingButCountsTotal(t *testing.T) {
	before := state(map[string]int64{"/": 2000}, 0, 0, nil)
	after := state(map[string]int64{"/": 1500}, 0, 0, nil) // volume shrank 500
	b := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep := Compute(before, after, b, b.Add(24*time.Hour), 0)
	if len(rep.Deltas) != 0 {
		t.Errorf("a shrinking volume should not rank as a grower: %+v", rep.Deltas)
	}
	// Total growth reflects the net change (library/caches 0 delta, volume -500).
	if rep.TotalGrowthBytes != -500 {
		t.Errorf("total growth = %d, want -500", rep.TotalGrowthBytes)
	}
}

func TestComputeTopN(t *testing.T) {
	before := state(map[string]int64{"/": 100, "/data": 100}, 100, 100, nil)
	after := state(map[string]int64{"/": 400, "/data": 300}, 250, 600, nil)
	rep := Compute(before, after, time.Unix(0, 0), time.Unix(0, 0).Add(24*time.Hour), 2)
	if len(rep.Deltas) != 2 {
		t.Fatalf("topN=2 gave %d", len(rep.Deltas))
	}
	// The two biggest growers: caches(+500), volume /(+300)? data +200, library +150.
	if rep.Deltas[0].GrowthBytes != 500 || rep.Deltas[1].GrowthBytes != 300 {
		t.Errorf("top-2 growth = %d,%d want 500,300", rep.Deltas[0].GrowthBytes, rep.Deltas[1].GrowthBytes)
	}
}

func TestComputeNonPositiveInterval(t *testing.T) {
	before := state(map[string]int64{"/": 100}, 0, 0, nil)
	after := state(map[string]int64{"/": 500}, 0, 0, nil)
	same := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rep := Compute(before, after, same, same, 0) // zero interval
	if rep.Note == "" {
		t.Error("expected a note about the non-positive interval")
	}
	if d, _ := deltaFor(rep, "volume", "/"); d.BytesPerDay != 0 {
		t.Errorf("rate should be 0 for a zero interval, got %v", d.BytesPerDay)
	}
}
