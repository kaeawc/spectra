package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestSaveAndGetFDSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	samples := []snapshot.FDSample{
		{PID: 1127, At: base, OpenFDs: 100},
		{PID: 1127, At: base.Add(time.Minute), OpenFDs: 130},
		{PID: 1127, At: base.Add(2 * time.Minute), OpenFDs: 170},
		{PID: 999, At: base.Add(time.Minute), OpenFDs: 30}, // unrelated PID
	}
	if err := db.SaveFDSamples(ctx, samples); err != nil {
		t.Fatalf("SaveFDSamples: %v", err)
	}

	got, err := db.GetRecentFDSamples(ctx, 1127, 0)
	if err != nil {
		t.Fatalf("GetRecentFDSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples for PID 1127, got %d", len(got))
	}
	for i, want := range []int{100, 130, 170} {
		if got[i].OpenFDs != want {
			t.Errorf("sample %d: OpenFDs = %v, want %v", i, got[i].OpenFDs, want)
		}
	}
	if !got[0].At.Equal(base) {
		t.Errorf("At roundtrip failed: got %v want %v", got[0].At, base)
	}
}

func TestGetRecentFDSamples_Limit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := db.SaveFDSamples(ctx, []snapshot.FDSample{{
			PID: 7, At: base.Add(time.Duration(i) * time.Minute), OpenFDs: i * 10,
		}}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	got, err := db.GetRecentFDSamples(ctx, 7, 3)
	if err != nil {
		t.Fatalf("GetRecentFDSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got))
	}
	// Should be the 3 newest, in oldest-first order: counts 70, 80, 90.
	for i, want := range []int{70, 80, 90} {
		if got[i].OpenFDs != want {
			t.Errorf("sample %d: OpenFDs = %v, want %v", i, got[i].OpenFDs, want)
		}
	}
}

func TestGetRecentFDSamples_None(t *testing.T) {
	db := openTestDB(t)
	got, err := db.GetRecentFDSamples(context.Background(), 12345, 0)
	if err != nil {
		t.Fatalf("GetRecentFDSamples: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for PID with no samples, got %v", got)
	}
}

func TestSaveFDSamples_Idempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	first := snapshot.FDSample{PID: 1, At: at, OpenFDs: 50}
	updated := snapshot.FDSample{PID: 1, At: at, OpenFDs: 95} // same key
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{first}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{updated}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ := db.GetRecentFDSamples(ctx, 1, 0)
	if len(got) != 1 || got[0].OpenFDs != 95 {
		t.Errorf("upsert should overwrite, got %v", got)
	}
}

func TestPruneFDSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old1 := snapshot.FDSample{PID: 1, At: now.Add(-30 * 24 * time.Hour), OpenFDs: 10}
	old2 := snapshot.FDSample{PID: 1, At: now.Add(-10 * 24 * time.Hour), OpenFDs: 20}
	recent := snapshot.FDSample{PID: 1, At: now.Add(-1 * time.Hour), OpenFDs: 90}
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{old1, old2, recent}); err != nil {
		t.Fatalf("save: %v", err)
	}
	deleted, err := db.PruneFDSamples(ctx, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted, got %d", deleted)
	}
	got, _ := db.GetRecentFDSamples(ctx, 1, 0)
	if len(got) != 1 || got[0].OpenFDs != 90 {
		t.Errorf("only the recent row should survive, got %v", got)
	}
}

func TestAttachFDHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)

	// Seed two prior samples for PID 42 so a window builds up.
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{
		{PID: 42, At: base, OpenFDs: 100},
		{PID: 42, At: base.Add(time.Minute), OpenFDs: 130},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	snap := &snapshot.Snapshot{
		TakenAt:   base.Add(2 * time.Minute),
		Processes: []process.Info{{PID: 42, OpenFDs: 170}},
	}
	db.AttachFDHistory(ctx, snap)

	got := snap.FDHistory.SamplesFor(42)
	if len(got) != 3 {
		t.Fatalf("expected 3 samples attached for PID 42, got %d", len(got))
	}
	for i, want := range []int{100, 130, 170} {
		if got[i].OpenFDs != want {
			t.Errorf("sample %d: OpenFDs = %v, want %v", i, got[i].OpenFDs, want)
		}
	}
}

func TestAttachFDHistory_NoOpenFDs(t *testing.T) {
	db := openTestDB(t)
	snap := &snapshot.Snapshot{
		TakenAt:   time.Now(),
		Processes: []process.Info{{PID: 42, OpenFDs: 0}},
	}
	db.AttachFDHistory(context.Background(), snap)
	if snap.FDHistory != nil {
		t.Errorf("expected no history for a snapshot with no open descriptors, got %v", snap.FDHistory)
	}
}
