package fleet

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func snap(id string, day int, apps ...detect.Result) snapshot.Snapshot {
	return snapshot.Snapshot{
		ID:      id,
		TakenAt: time.Date(2026, 8, day, 9, 0, 0, 0, time.UTC),
		Apps:    apps,
	}
}

func signed() detect.Result {
	return detect.Result{Path: "/Applications/Foo.app", BundleID: "com.x.foo", TeamID: "TEAM1", AppVersion: "2.3"}
}
func unsigned() detect.Result {
	return detect.Result{Path: "/Applications/Foo.app", BundleID: "com.x.foo", TeamID: "", AppVersion: "2.4"}
}

func TestBisectFound(t *testing.T) {
	series := []snapshot.Snapshot{
		snap("s1", 1, signed()),
		snap("s2", 2, unsigned()), // app-unsigned starts firing here; team-id + version co-change
		snap("s3", 3, unsigned()),
	}
	r := BisectSymptom(series, "app-unsigned", rules.V1Catalog())
	if r.Status != "found" {
		t.Fatalf("status = %q, want found", r.Status)
	}
	if r.FirstBadID != "s2" || r.PrevID != "s1" {
		t.Errorf("first=%q prev=%q, want s2/s1", r.FirstBadID, r.PrevID)
	}
	if len(r.Changes) == 0 {
		t.Error("expected co-occurring changes between s1 and s2")
	}
}

func TestBisectClean(t *testing.T) {
	series := []snapshot.Snapshot{snap("s1", 1, signed()), snap("s2", 2, signed())}
	r := BisectSymptom(series, "app-unsigned", rules.V1Catalog())
	if r.Status != "clean" {
		t.Errorf("status = %q, want clean", r.Status)
	}
	if r.FirstBadID != "" {
		t.Errorf("clean series should have no first-bad id, got %q", r.FirstBadID)
	}
}

func TestBisectAlreadyFiring(t *testing.T) {
	series := []snapshot.Snapshot{snap("s1", 1, unsigned()), snap("s2", 2, unsigned())}
	r := BisectSymptom(series, "app-unsigned", rules.V1Catalog())
	if r.Status != "already-firing" {
		t.Errorf("status = %q, want already-firing", r.Status)
	}
	if r.FirstBadID != "s1" || r.PrevID != "" {
		t.Errorf("first=%q prev=%q, want s1/'' (no earlier snapshot)", r.FirstBadID, r.PrevID)
	}
}
