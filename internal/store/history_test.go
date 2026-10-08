package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

var historyBase = time.Date(2026, 5, 8, 10, 0, 0, 0, time.UTC)

func TestPruneSamples_UsesInjectedClock(t *testing.T) {
	clk := clock.NewFake(historyBase)
	db := openTestDBWithOptions(t, Options{Clock: clk})
	ctx := context.Background()
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{PID: 1, At: historyBase, OldGenPct: 10}}); err != nil {
		t.Fatalf("save jvm: %v", err)
	}
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{{PID: 1, At: historyBase, OpenFDs: 10}}); err != nil {
		t.Fatalf("save fd: %v", err)
	}
	prunes := map[string]func(context.Context, int) (int64, error){
		"jvm": db.PruneJVMSamples,
		"fd":  db.PruneFDSamples,
	}

	clk.Advance(6 * 24 * time.Hour)
	for name, prune := range prunes {
		if n, err := prune(ctx, 7); err != nil || n != 0 {
			t.Fatalf("%s prune at +6d = (%d, %v), want (0, nil)", name, n, err)
		}
	}
	clk.Advance(2 * 24 * time.Hour)
	for name, prune := range prunes {
		if n, err := prune(ctx, 7); err != nil || n != 1 {
			t.Fatalf("%s prune at +8d = (%d, %v), want (1, nil)", name, n, err)
		}
	}
}

func TestAttachHistory_ZeroTakenAtUsesInjectedClock(t *testing.T) {
	db := openTestDBWithOptions(t, Options{Clock: clock.NewFake(historyBase)})
	ctx := context.Background()
	snap := &snapshot.Snapshot{
		JVMs:      []jvm.Info{{PID: 42, GC: &jvm.GCStats{OC: 100, OU: 50}}},
		Processes: []process.Info{{PID: 42, OpenFDs: 17}},
	}
	if err := db.AttachJVMHistory(ctx, snap); err != nil {
		t.Fatalf("AttachJVMHistory: %v", err)
	}
	if err := db.AttachFDHistory(ctx, snap); err != nil {
		t.Fatalf("AttachFDHistory: %v", err)
	}

	if got := snap.JVMHistory.SamplesFor(42); len(got) != 1 || !got[0].At.Equal(historyBase) {
		t.Errorf("jvm history = %v, want one sample at %v", got, historyBase)
	}
	if got := snap.FDHistory.SamplesFor(42); len(got) != 1 || !got[0].At.Equal(historyBase) {
		t.Errorf("fd history = %v, want one sample at %v", got, historyBase)
	}
}

// failInserts makes every INSERT into table abort while leaving reads intact,
// so tests can exercise a failed sample write against a readable store.
func failInserts(t *testing.T, db *DB, table string) {
	t.Helper()
	stmt := `CREATE TRIGGER fail_` + table + ` BEFORE INSERT ON ` + table +
		` BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`
	if _, err := db.db.Exec(stmt); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
}

func TestAttachJVMHistory_FailedWriteSkipsHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{
		{PID: 42, At: historyBase, OldGenPct: 40},
		{PID: 42, At: historyBase.Add(time.Minute), OldGenPct: 60},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	failInserts(t, db, "jvm_samples")

	snap := &snapshot.Snapshot{
		TakenAt:    historyBase.Add(2 * time.Minute),
		JVMs:       []jvm.Info{{PID: 42, GC: &jvm.GCStats{OC: 100, OU: 80}}},
		JVMHistory: snapshot.JVMHistory{{PID: 42, OldGenPct: 99}},
	}
	if err := db.AttachJVMHistory(ctx, snap); err == nil {
		t.Fatal("expected error from failed sample write")
	}
	if snap.JVMHistory != nil {
		t.Errorf("history must not be loaded after a failed write, got %v", snap.JVMHistory)
	}
}

func TestAttachFDHistory_FailedWriteSkipsHistory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{
		{PID: 42, At: historyBase, OpenFDs: 100},
		{PID: 42, At: historyBase.Add(time.Minute), OpenFDs: 200},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	failInserts(t, db, "fd_samples")

	snap := &snapshot.Snapshot{
		TakenAt:   historyBase.Add(2 * time.Minute),
		Processes: []process.Info{{PID: 42, OpenFDs: 300}},
		FDHistory: snapshot.FDHistory{{PID: 42, OpenFDs: 999}},
	}
	if err := db.AttachFDHistory(ctx, snap); err == nil {
		t.Fatal("expected error from failed sample write")
	}
	if snap.FDHistory != nil {
		t.Errorf("history must not be loaded after a failed write, got %v", snap.FDHistory)
	}
}
