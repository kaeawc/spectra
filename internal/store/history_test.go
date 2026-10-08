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
	db.AttachJVMHistory(ctx, snap)
	db.AttachFDHistory(ctx, snap)

	if got := snap.JVMHistory.SamplesFor(42); len(got) != 1 || !got[0].At.Equal(historyBase) {
		t.Errorf("jvm history = %v, want one sample at %v", got, historyBase)
	}
	if got := snap.FDHistory.SamplesFor(42); len(got) != 1 || !got[0].At.Equal(historyBase) {
		t.Errorf("fd history = %v, want one sample at %v", got, historyBase)
	}
}
