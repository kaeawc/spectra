package store

import (
	"context"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// jstat can fail (e.g. -XX:-UsePerfData) while jcmd NMT succeeds: the NMT
// reading must still build history, without a zero-filled GC row.
func TestAttachJVMHistory_NMTOnlyWhenGCUnavailable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		snap := &snapshot.Snapshot{
			TakenAt: historyBase.Add(time.Duration(i) * 10 * time.Minute),
			JVMs: []jvm.Info{{
				PID: 42, NMTCommittedKiB: map[string]int64{"Internal": int64(1000 * (i + 1))},
			}},
		}
		if err := db.AttachJVMHistory(ctx, snap); err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		if i < 2 {
			continue
		}
		got := snap.JVMHistory.NMTSamplesFor(42)
		if len(got) != 3 {
			t.Fatalf("nmt samples = %d, want 3: %+v", len(got), got)
		}
		for k, sm := range got {
			if !sm.NMTOnly || sm.NMTCommittedKiB["Internal"] != int64(1000*(k+1)) {
				t.Errorf("sample %d = %+v", k, sm)
			}
		}
		if gc := snap.JVMHistory.SamplesFor(42); len(gc) != 0 {
			t.Errorf("GC trend samples must exclude NMT-only: %+v", gc)
		}
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM jvm_samples`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jvm_samples rows = %d, %v; want 0 (no zero GC rows)", n, err)
	}
}

// Linux start times jitter by about a second between scans: an NMT-only series
// must stay joined, while a clearly different lifetime stays separate.
func TestLoadNMTOnlySamples_ToleratesStartJitter(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	start := historyBase.Add(-time.Hour)
	for i, jitter := range []time.Duration{0, time.Second, -time.Second} {
		if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{
			PID: 42, ProcStart: start.Add(jitter), At: historyBase.Add(time.Duration(i) * time.Minute),
			NMTOnly: true, NMTCommittedKiB: map[string]int64{"Other": int64(i + 1)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.loadNMTOnlySamples(ctx, 42, start.Add(time.Second))
	if err != nil || len(got) != 3 {
		t.Fatalf("jittered lifetime = (%d samples, %v), want 3", len(got), err)
	}
	if far, _ := db.loadNMTOnlySamples(ctx, 42, start.Add(10*time.Minute)); len(far) != 0 {
		t.Fatalf("distant lifetime inherited samples: %v", far)
	}
	if unknown, _ := db.loadNMTOnlySamples(ctx, 42, time.Time{}); len(unknown) != 0 {
		t.Fatalf("unknown start inherited known-start samples: %v", unknown)
	}
}

func TestLoadNMTOnlySamples_IsolatedByProcessLifetime(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	start := historyBase.Add(-time.Hour)
	if err := db.SaveJVMSamples(ctx, []snapshot.JVMSample{{
		PID: 42, ProcStart: start, At: historyBase, NMTOnly: true,
		NMTCommittedKiB: map[string]int64{"Other": 5},
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.loadNMTOnlySamples(ctx, 42, start)
	if err != nil || len(got) != 1 {
		t.Fatalf("own lifetime = (%v, %v)", got, err)
	}
	if other, _ := db.loadNMTOnlySamples(ctx, 42, historyBase.Add(-2*time.Hour)); len(other) != 0 {
		t.Fatalf("reused PID inherited NMT-only samples: %v", other)
	}
}
