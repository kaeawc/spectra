package store

import (
	"context"
	"database/sql"
	"path/filepath"
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

func TestAttachJVMHistory_ReusedPIDDoesNotInheritSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	deadStart := historyBase.Add(-time.Hour)
	newStart := historyBase.Add(90 * time.Second)
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{
		{PID: 42, ProcStart: deadStart, At: historyBase, OldGenPct: 40},
		{PID: 42, ProcStart: deadStart, At: historyBase.Add(time.Minute), OldGenPct: 60},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	snap := &snapshot.Snapshot{
		TakenAt:   historyBase.Add(2 * time.Minute),
		Processes: []process.Info{{PID: 42, StartTime: newStart}},
		JVMs:      []jvm.Info{{PID: 42, GC: &jvm.GCStats{OC: 100, OU: 80}}},
	}
	if err := db.AttachJVMHistory(ctx, snap); err != nil {
		t.Fatalf("AttachJVMHistory: %v", err)
	}
	got := snap.JVMHistory.SamplesFor(42)
	if len(got) != 1 || got[0].OldGenPct != 80 || !got[0].ProcStart.Equal(newStart) {
		t.Fatalf("history = %+v, want only the new process's sample", got)
	}

	// The dead process's samples remain addressable by their own identity.
	old, err := db.GetRecentJVMSamples(ctx, 42, deadStart, 0)
	if err != nil || len(old) != 2 {
		t.Fatalf("dead-process samples = (%d, %v), want 2", len(old), err)
	}
}

func TestAttachFDHistory_ReusedPIDDoesNotInheritSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	deadStart := historyBase.Add(-time.Hour)
	newStart := historyBase.Add(90 * time.Second)
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{
		{PID: 42, ProcStart: deadStart, At: historyBase, OpenFDs: 100},
		{PID: 42, ProcStart: deadStart, At: historyBase.Add(time.Minute), OpenFDs: 500},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	snap := &snapshot.Snapshot{
		TakenAt:   historyBase.Add(2 * time.Minute),
		Processes: []process.Info{{PID: 42, StartTime: newStart, OpenFDs: 20}},
	}
	if err := db.AttachFDHistory(ctx, snap); err != nil {
		t.Fatalf("AttachFDHistory: %v", err)
	}
	got := snap.FDHistory.SamplesFor(42)
	if len(got) != 1 || got[0].OpenFDs != 20 || !got[0].ProcStart.Equal(newStart) {
		t.Fatalf("history = %+v, want only the new process's sample", got)
	}
}

func TestAttachFDHistory_SameProcessAccumulates(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	// Sub-second start precision must not split one process's history.
	start := historyBase.Add(-time.Hour + 250*time.Millisecond)
	for i, fds := range []int{100, 200, 300} {
		snap := &snapshot.Snapshot{
			TakenAt:   historyBase.Add(time.Duration(i) * time.Minute),
			Processes: []process.Info{{PID: 42, StartTime: start, OpenFDs: fds}},
		}
		if err := db.AttachFDHistory(ctx, snap); err != nil {
			t.Fatalf("AttachFDHistory #%d: %v", i, err)
		}
		if got := len(snap.FDHistory.SamplesFor(42)); got != i+1 {
			t.Fatalf("after attach #%d: %d samples, want %d", i, got, i+1)
		}
	}
}

func TestAttachHistory_UnknownStartIgnoresIdentifiedSamples(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SaveFDSamples(ctx, []snapshot.FDSample{
		{PID: 42, ProcStart: historyBase.Add(-time.Hour), At: historyBase, OpenFDs: 100},
		{PID: 42, At: historyBase.Add(time.Minute), OpenFDs: 110},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snap := &snapshot.Snapshot{
		TakenAt:   historyBase.Add(2 * time.Minute),
		Processes: []process.Info{{PID: 42, OpenFDs: 120}},
	}
	if err := db.AttachFDHistory(ctx, snap); err != nil {
		t.Fatalf("AttachFDHistory: %v", err)
	}
	got := snap.FDHistory.SamplesFor(42)
	if len(got) != 2 || got[0].OpenFDs != 110 || got[1].OpenFDs != 120 {
		t.Fatalf("history = %+v, want only unknown-start samples [110 120]", got)
	}
}

func TestMigrateAddsProcStartToLegacySampleTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE jvm_samples (pid INTEGER NOT NULL, at_nano INTEGER NOT NULL,
			old_gen_pct REAL NOT NULL DEFAULT 0, fgc INTEGER NOT NULL DEFAULT 0,
			fgct REAL NOT NULL DEFAULT 0, heap_mb INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (pid, at_nano))`,
		`CREATE TABLE fd_samples (pid INTEGER NOT NULL, at_nano INTEGER NOT NULL,
			open_fds INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (pid, at_nano))`,
		`INSERT INTO jvm_samples (pid, at_nano, old_gen_pct) VALUES (42, 1, 50)`,
		`INSERT INTO fd_samples (pid, at_nano, open_fds) VALUES (42, 1, 10)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q: %v", stmt, err)
		}
	}
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	jvms, err := db.GetRecentJVMSamples(ctx, 42, time.Time{}, 0)
	if err != nil || len(jvms) != 1 || !jvms[0].ProcStart.IsZero() {
		t.Fatalf("legacy jvm rows = (%+v, %v), want one unknown-start row", jvms, err)
	}
	fds, err := db.GetRecentFDSamples(ctx, 42, time.Time{}, 0)
	if err != nil || len(fds) != 1 || !fds[0].ProcStart.IsZero() {
		t.Fatalf("legacy fd rows = (%+v, %v), want one unknown-start row", fds, err)
	}
	if got, _ := db.GetRecentFDSamples(ctx, 42, historyBase, 0); got != nil {
		t.Fatalf("identified process must not see legacy rows, got %+v", got)
	}

	// Reopening an already-migrated DB is a no-op.
	db.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen migrated: %v", err)
	}
	reopened.Close()
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
