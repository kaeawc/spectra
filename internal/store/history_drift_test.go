package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestAttachJVMHistory_StartTimeJitterKeepsHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	start := historyBase.Add(-time.Hour)
	for i, jitter := range []time.Duration{0, time.Second, -time.Second} {
		snap := &snapshot.Snapshot{
			TakenAt:   historyBase.Add(time.Duration(i) * time.Minute),
			Processes: []process.Info{{PID: 42, StartTime: start.Add(jitter)}},
			JVMs:      []jvm.Info{{PID: 42, GC: &jvm.GCStats{OC: 100, OU: 50}}},
		}
		if err := db.AttachJVMHistory(ctx, snap); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		if got := snap.JVMHistory.SamplesFor(42); len(got) != i+1 {
			t.Fatalf("attach %d: history len = %d, want %d", i, len(got), i+1)
		}
	}
}

func TestAttachFDHistory_StartTimeJitterKeepsHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	start := historyBase.Add(-time.Hour)
	for i, jitter := range []time.Duration{0, time.Second, -time.Second} {
		snap := &snapshot.Snapshot{
			TakenAt:   historyBase.Add(time.Duration(i) * time.Minute),
			Processes: []process.Info{{PID: 42, StartTime: start.Add(jitter), OpenFDs: 10 + i}},
		}
		if err := db.AttachFDHistory(ctx, snap); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		if got := snap.FDHistory.SamplesFor(42); len(got) != i+1 {
			t.Fatalf("attach %d: history len = %d, want %d", i, len(got), i+1)
		}
	}
}

func TestHistory_DistinctLifetimesAndUnknownStaySeparate(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	a := historyBase.Add(-time.Hour)
	b := a.Add(10 * time.Second)
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{
		{PID: 7, ProcStart: a, At: historyBase},
		{PID: 7, ProcStart: b, At: historyBase.Add(time.Minute)},
		{PID: 7, At: historyBase.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("save jvm: %v", err)
	}
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{
		{PID: 7, ProcStart: a, At: historyBase},
		{PID: 7, ProcStart: b, At: historyBase.Add(time.Minute)},
		{PID: 7, At: historyBase.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("save fd: %v", err)
	}
	for name, start := range map[string]time.Time{"a": a, "b": b, "unknown": {}} {
		j, err := db.GetRecentJVMSamples(ctx, 7, start, 0)
		if err != nil || len(j) != 1 {
			t.Errorf("jvm %s = (%d, %v), want 1", name, len(j), err)
		}
		f, err := db.GetRecentFDSamples(ctx, 7, start, 0)
		if err != nil || len(f) != 1 {
			t.Errorf("fd %s = (%d, %v), want 1", name, len(f), err)
		}
	}
}

func TestProcStartRange(t *testing.T) {
	if lo, hi := procStartRange(time.Time{}); lo != 0 || hi != 0 {
		t.Errorf("zero range = [%d,%d], want [0,0]", lo, hi)
	}
	u := historyBase.Unix()
	if lo, hi := procStartRange(historyBase); lo != u-2 || hi != u+2 {
		t.Errorf("range = [%d,%d], want [%d,%d]", lo, hi, u-2, u+2)
	}
}
