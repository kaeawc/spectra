package snapshot

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestFDHistory_SamplesFor(t *testing.T) {
	now := time.Now()
	h := FDHistory{
		{PID: 10, At: now.Add(-3 * time.Minute), OpenFDs: 100},
		{PID: 11, At: now.Add(-3 * time.Minute), OpenFDs: 50},
		{PID: 10, At: now.Add(-2 * time.Minute), OpenFDs: 120},
		{PID: 10, At: now.Add(-1 * time.Minute), OpenFDs: 140},
	}
	got := h.SamplesFor(10)
	if len(got) != 3 {
		t.Fatalf("expected 3 samples for PID 10, got %d", len(got))
	}
	for i, want := range []int{100, 120, 140} {
		if got[i].OpenFDs != want {
			t.Errorf("sample %d: OpenFDs = %v, want %v", i, got[i].OpenFDs, want)
		}
	}
}

func TestFDHistory_SamplesForNoMatch(t *testing.T) {
	h := FDHistory{{PID: 10, OpenFDs: 50}}
	if got := h.SamplesFor(99); got != nil {
		t.Errorf("expected nil for missing PID, got %v", got)
	}
}

func TestFDHistory_SamplesForEmpty(t *testing.T) {
	var h FDHistory
	if got := h.SamplesFor(10); got != nil {
		t.Errorf("expected nil from empty history, got %v", got)
	}
}

func TestFDSampleFrom(t *testing.T) {
	at := time.Date(2026, 5, 8, 10, 0, 0, 0, time.Local)
	sm, ok := FDSampleFrom(process.Info{PID: 42, OpenFDs: 128}, at)
	if !ok {
		t.Fatal("expected ok for a process with open descriptors")
	}
	if sm.PID != 42 || sm.OpenFDs != 128 {
		t.Errorf("unexpected sample: %+v", sm)
	}
	if !sm.At.Equal(at) {
		t.Errorf("At = %v, want %v", sm.At, at)
	}
	if sm.At.Location() != time.UTC {
		t.Errorf("At should be normalized to UTC, got %v", sm.At.Location())
	}
}

func TestFDSampleFrom_NoDescriptors(t *testing.T) {
	if _, ok := FDSampleFrom(process.Info{PID: 42, OpenFDs: 0}, time.Now()); ok {
		t.Error("expected ok=false when OpenFDs is 0 (shallow mode)")
	}
	if _, ok := FDSampleFrom(process.Info{PID: 42, OpenFDs: -1}, time.Now()); ok {
		t.Error("expected ok=false when OpenFDs is negative")
	}
}
