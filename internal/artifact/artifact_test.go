package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/clock"
)

func TestManagerRecordDefaultsAndPersists(t *testing.T) {
	dir := t.TempDir()
	blob := filepath.Join(dir, "heap.hprof")
	if err := os.WriteFile(blob, []byte("heap"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewJSONStore(filepath.Join(dir, "artifacts.json"))
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	mgr := NewManager(store, clock.NewFake(now))

	rec, err := mgr.Record(context.Background(), Record{
		Kind:    KindHeapDump,
		Source:  "cli",
		Command: "spectra jvm heap-dump",
		Path:    blob,
		PID:     42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID == "" {
		t.Fatal("expected generated id")
	}
	if rec.Sensitivity != SensitivityVeryHigh {
		t.Fatalf("sensitivity = %q, want %q", rec.Sensitivity, SensitivityVeryHigh)
	}
	if rec.SizeBytes != 4 {
		t.Fatalf("size = %d, want 4", rec.SizeBytes)
	}
	if !rec.CreatedAt.Equal(now) {
		t.Fatalf("created_at = %s, want %s", rec.CreatedAt, now)
	}

	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].ID != rec.ID {
		t.Fatalf("stored id = %q, want %q", records[0].ID, rec.ID)
	}
}

func TestJSONStoreAppendPreservesExistingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifacts.json")
	store := NewJSONStore(path)

	for _, rec := range []Record{
		{ID: "one", Kind: KindJFRRecording, CreatedAt: time.Unix(1, 0).UTC()},
		{ID: "two", Kind: KindProcessSample, CreatedAt: time.Unix(2, 0).UTC()},
	} {
		if err := store.Append(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}

	records, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].ID != "one" || records[1].ID != "two" {
		t.Fatalf("records out of order: %#v", records)
	}
}

func TestFakeRecorder(t *testing.T) {
	fake := &FakeRecorder{}
	if _, err := fake.Record(context.Background(), Record{Kind: KindPacketCapture}); err != nil {
		t.Fatal(err)
	}
	got := fake.Snapshot()
	if len(got) != 1 || got[0].Kind != KindPacketCapture {
		t.Fatalf("snapshot = %#v", got)
	}
	got[0].Kind = "mutated"
	if fake.Snapshot()[0].Kind != KindPacketCapture {
		t.Fatal("snapshot exposed internal storage")
	}
}

func TestPolicyAuthorize(t *testing.T) {
	rec := Record{Kind: KindHeapDump}
	if err := (Policy{}).Authorize(rec, false); err == nil {
		t.Fatal("default policy allowed unconfirmed artifact")
	}
	if err := (Policy{}).Authorize(rec, true); err != nil {
		t.Fatalf("default policy rejected confirmed artifact: %v", err)
	}
	if err := (Policy{Mode: PolicyDeny}).Authorize(rec, true); err == nil {
		t.Fatal("deny policy allowed artifact")
	}
	if err := (Policy{Mode: PolicyAllow}).Authorize(rec, false); err != nil {
		t.Fatalf("allow policy rejected artifact: %v", err)
	}
	if err := (Policy{Mode: "bogus"}).Validate(); err == nil {
		t.Fatal("invalid policy mode validated")
	}
}
