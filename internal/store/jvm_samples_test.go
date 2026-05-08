package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestSaveAndGetJVMSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	samples := []snapshot.JVMSample{
		{PID: 1127, At: base, OldGenPct: 70, FGC: 100, FGCT: 5.0, HeapMB: 190},
		{PID: 1127, At: base.Add(time.Minute), OldGenPct: 80, FGC: 102, FGCT: 5.4},
		{PID: 1127, At: base.Add(2 * time.Minute), OldGenPct: 90, FGC: 105, FGCT: 5.9},
		{PID: 999, At: base.Add(time.Minute), OldGenPct: 30}, // unrelated PID
	}
	if err := db.SaveJVMSamples(ctx, samples); err != nil {
		t.Fatalf("SaveJVMSamples: %v", err)
	}

	got, err := db.GetRecentJVMSamples(ctx, 1127, 0)
	if err != nil {
		t.Fatalf("GetRecentJVMSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples for PID 1127, got %d", len(got))
	}
	for i, want := range []float64{70, 80, 90} {
		if got[i].OldGenPct != want {
			t.Errorf("sample %d: OldGenPct = %v, want %v", i, got[i].OldGenPct, want)
		}
	}
	if got[0].HeapMB != 190 {
		t.Errorf("HeapMB roundtrip failed: %v", got[0].HeapMB)
	}
	if !got[0].At.Equal(base) {
		t.Errorf("At roundtrip failed: got %v want %v", got[0].At, base)
	}
}

func TestGetRecentJVMSamples_Limit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{
			PID: 7, At: base.Add(time.Duration(i) * time.Minute), OldGenPct: float64(i * 10),
		}}); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	got, err := db.GetRecentJVMSamples(ctx, 7, 3)
	if err != nil {
		t.Fatalf("GetRecentJVMSamples: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got))
	}
	// Should be the 3 newest, in oldest-first order: pcts 70, 80, 90.
	for i, want := range []float64{70, 80, 90} {
		if got[i].OldGenPct != want {
			t.Errorf("sample %d: OldGenPct = %v, want %v", i, got[i].OldGenPct, want)
		}
	}
}

func TestGetRecentJVMSamples_None(t *testing.T) {
	db := openTestDB(t)
	got, err := db.GetRecentJVMSamples(context.Background(), 12345, 0)
	if err != nil {
		t.Fatalf("GetRecentJVMSamples: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for PID with no samples, got %v", got)
	}
}

func TestSaveJVMSamples_Idempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	at := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	first := snapshot.JVMSample{PID: 1, At: at, OldGenPct: 50}
	updated := snapshot.JVMSample{PID: 1, At: at, OldGenPct: 95} // same key
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{first}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{updated}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ := db.GetRecentJVMSamples(ctx, 1, 0)
	if len(got) != 1 || got[0].OldGenPct != 95 {
		t.Errorf("upsert should overwrite, got %v", got)
	}
}

// Parallel diagnose calls landing within the same wall-clock second must
// produce distinct rows because at_nano is the timestamp granularity.
func TestSaveJVMSamples_SubSecondDistinct(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)
	one := snapshot.JVMSample{PID: 1, At: base.Add(100_000), OldGenPct: 60}     // +100µs
	two := snapshot.JVMSample{PID: 1, At: base.Add(200_000), OldGenPct: 65}     // +200µs
	three := snapshot.JVMSample{PID: 1, At: base.Add(300_000), OldGenPct: 70}   // +300µs
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{one, two, three}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := db.GetRecentJVMSamples(ctx, 1, 0)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct sub-second rows, got %d", len(got))
	}
}

func TestPruneJVMSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old1 := snapshot.JVMSample{PID: 1, At: now.Add(-30 * 24 * time.Hour), OldGenPct: 10}
	old2 := snapshot.JVMSample{PID: 1, At: now.Add(-10 * 24 * time.Hour), OldGenPct: 20}
	recent := snapshot.JVMSample{PID: 1, At: now.Add(-1 * time.Hour), OldGenPct: 90}
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{old1, old2, recent}); err != nil {
		t.Fatalf("save: %v", err)
	}
	deleted, err := db.PruneJVMSamples(ctx, 7)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted, got %d", deleted)
	}
	got, _ := db.GetRecentJVMSamples(ctx, 1, 0)
	if len(got) != 1 || got[0].OldGenPct != 90 {
		t.Errorf("only the recent row should survive, got %v", got)
	}
}
