package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestAttachJVMHistory_NMTRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{
		{PID: 42, At: historyBase, NMTCommittedKiB: map[string]int64{"Internal": 1000, "Thread": 500}},
		{PID: 42, At: historyBase.Add(time.Minute)}, // NMT off / failed sample
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	snap := &snapshot.Snapshot{
		TakenAt: historyBase.Add(2 * time.Minute),
		JVMs: []jvm.Info{{
			PID: 42, GC: &jvm.GCStats{OC: 100, OU: 10},
			NMTCommittedKiB: map[string]int64{"Internal": 3000},
		}},
	}
	if err := db.AttachJVMHistory(ctx, snap); err != nil {
		t.Fatalf("AttachJVMHistory: %v", err)
	}
	got := snap.JVMHistory.SamplesFor(42)
	if len(got) != 3 {
		t.Fatalf("samples = %d, want 3", len(got))
	}
	if got[0].NMTCommittedKiB["Internal"] != 1000 || got[0].NMTCommittedKiB["Thread"] != 500 {
		t.Errorf("first sample nmt = %v", got[0].NMTCommittedKiB)
	}
	if got[1].NMTCommittedKiB != nil {
		t.Errorf("second sample nmt = %v, want nil", got[1].NMTCommittedKiB)
	}
	if got[2].NMTCommittedKiB["Internal"] != 3000 {
		t.Errorf("current sample nmt = %v", got[2].NMTCommittedKiB)
	}
}

func TestSaveJVMSamples_NMTUpsertAndClear(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	at := historyBase
	save := func(cats map[string]int64) {
		t.Helper()
		if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{PID: 7, At: at, NMTCommittedKiB: cats}}); err != nil {
			t.Fatal(err)
		}
	}
	load := func() map[string]int64 {
		t.Helper()
		got, err := db.GetRecentJVMSamples(ctx, 7, time.Time{}, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("get = (%v, %v)", got, err)
		}
		if err := db.loadNMTSamples(ctx, 7, got); err != nil {
			t.Fatal(err)
		}
		return got[0].NMTCommittedKiB
	}
	save(map[string]int64{"Class": 1})
	save(map[string]int64{"Class": 2})
	if got := load(); got["Class"] != 2 {
		t.Fatalf("upsert: %v", got)
	}
	save(nil)
	if got := load(); got != nil {
		t.Fatalf("clear: %v", got)
	}
}

func TestPruneJVMSamples_RemovesNMTRows(t *testing.T) {
	clk := clock.NewFake(historyBase)
	db := openTestDBWithOptions(t, Options{Clock: clk})
	ctx := context.Background()
	old := historyBase.Add(-30 * 24 * time.Hour)
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{
		{PID: 1, At: old, NMTCommittedKiB: map[string]int64{"Class": 1}},
		{PID: 1, At: historyBase, NMTCommittedKiB: map[string]int64{"Class": 2}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PruneJVMSamples(ctx, 7); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM jvm_nmt_samples`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("nmt rows after prune = %d, %v; want 1", n, err)
	}
}

// A database created before NMT sampling has jvm_samples but no
// jvm_nmt_samples; opening it must add the table, keep the old rows readable,
// and be idempotent on reopen.
func TestMigrateAddsNMTSamplesTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-nmt.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE jvm_samples (pid INTEGER NOT NULL, proc_start_unix INTEGER NOT NULL DEFAULT 0,
			at_nano INTEGER NOT NULL, old_gen_pct REAL NOT NULL DEFAULT 0, fgc INTEGER NOT NULL DEFAULT 0,
			fgct REAL NOT NULL DEFAULT 0, heap_mb INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (pid, at_nano))`,
		`INSERT INTO jvm_samples (pid, at_nano, old_gen_pct) VALUES (42, 1, 50)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	raw.Close()

	for i := 0; i < 2; i++ { // second pass proves idempotency
		db, err := Open(path)
		if err != nil {
			t.Fatalf("Open pass %d: %v", i, err)
		}
		got, err := db.GetRecentJVMSamples(context.Background(), 42, time.Time{}, 0)
		if err != nil || len(got) != 1 {
			t.Fatalf("pass %d legacy rows = (%v, %v)", i, got, err)
		}
		if err := db.loadNMTSamples(context.Background(), 42, got); err != nil || got[0].NMTCommittedKiB != nil {
			t.Fatalf("pass %d nmt = (%v, %v)", i, got[0].NMTCommittedKiB, err)
		}
		db.Close()
	}
}
