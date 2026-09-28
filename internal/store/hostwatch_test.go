package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

func TestHostwatchStoreCRUDAndPrune(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewFake(at)
	db, err := OpenWithOptions(filepath.Join(t.TempDir(), "watch.db"), Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.SaveHostSample(ctx, at.Add(-8*24*time.Hour), []byte(`{"old":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveHostSample(ctx, at, []byte(`{"now":1}`)); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListHostSamples(ctx, time.Time{}, 10)
	if err != nil || len(rows) != 2 || string(rows[0].JSON) != `{"now":1}` {
		t.Fatalf("samples %+v %v", rows, err)
	}
	n, err := db.PruneHostSamples(ctx, 7)
	if err != nil || n != 1 {
		t.Fatalf("prune samples %d %v", n, err)
	}
	a := AlertRow{ID: "a", Key: "load.high", Severity: "warn", Title: "high", Detail: "detail", State: "firing", FiredAt: at.Add(-40 * 24 * time.Hour)}
	if err = db.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListAlerts(ctx, "firing", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("alerts %+v %v", got, err)
	}
	acked, err := db.AckAlert(ctx, "a", at)
	if err != nil || acked.AckedAt == nil {
		t.Fatalf("ack %+v %v", acked, err)
	}
	_, err = db.AckAlert(ctx, "missing", at)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	resolved := at.Add(-31 * 24 * time.Hour)
	a.State = "resolved"
	a.ResolvedAt = &resolved
	if err = db.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	n, err = db.PruneResolvedAlerts(ctx, 30)
	if err != nil || n != 1 {
		t.Fatalf("prune alerts %d %v", n, err)
	}
	got, err = db.ListAlerts(ctx, "all", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("remaining %+v %v", got, err)
	}
}
