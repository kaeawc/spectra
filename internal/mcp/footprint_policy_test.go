package mcp

import (
	"context"
	"testing"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// Snapshots that can be persisted (create/baseline) must read the ~/Library
// footprint fresh-only, so storage growth between them is real.
func TestSnapshotCreateUsesFreshOnlyFootprint(t *testing.T) {
	for _, op := range []string{"create", "baseline"} {
		opts := snapshot.Options{FootprintPolicy: snapshot.FootprintRefresh} // sentinel
		s := newDeadlockTestServer(&opts)
		if res := s.toolSnapshotCreate(context.Background(), snapshotParams{Operation: op}); res.IsError {
			t.Fatalf("%s: %+v", op, res.Content)
		}
		if opts.FootprintPolicy != snapshot.FootprintFreshOnly {
			t.Errorf("%s: FootprintPolicy = %v, want FreshOnly", op, opts.FootprintPolicy)
		}
	}
}

func TestLiveRulesSnapshotMayServeStaleFootprint(t *testing.T) {
	var opts snapshot.Options
	s := newDeadlockTestServer(&opts)
	if _, _, err := s.evaluateRules(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if opts.FootprintPolicy != snapshot.FootprintServeStale {
		t.Errorf("FootprintPolicy = %v, want ServeStale", opts.FootprintPolicy)
	}
}

func TestDefaultCollectorsShareOneFootprintCache(t *testing.T) {
	c := defaultCollectors()
	snaps, ok1 := c.Snapshots.(defaultSnapshotCollector)
	storage, ok2 := c.Storage.(defaultStorageCollector)
	if !ok1 || !ok2 || snaps.footprint == nil || snaps.footprint != storage.footprint {
		t.Fatal("storage and snapshot tools must share one footprint cache")
	}
}
