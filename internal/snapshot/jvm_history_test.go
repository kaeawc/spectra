package snapshot

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/jvm"
)

func TestJVMSampleFrom_CopiesNMTCategories(t *testing.T) {
	info := jvm.Info{PID: 5, GC: &jvm.GCStats{OC: 10}, NMTCommittedKiB: map[string]int64{"Thread": 64}}
	s, ok := JVMSampleFrom(info, time.Unix(100, 0))
	if !ok || s.NMTCommittedKiB["Thread"] != 64 {
		t.Fatalf("sample = %+v ok=%v", s, ok)
	}
	info.NMTCommittedKiB["Thread"] = 999 // sample must not alias the live map
	if s.NMTCommittedKiB["Thread"] != 64 {
		t.Fatal("sample aliases Info map")
	}
}

func TestJVMHistory_SamplesFor(t *testing.T) {
	now := time.Now()
	h := JVMHistory{
		{PID: 10, At: now.Add(-3 * time.Minute), OldGenPct: 70},
		{PID: 11, At: now.Add(-3 * time.Minute), OldGenPct: 50},
		{PID: 10, At: now.Add(-2 * time.Minute), OldGenPct: 80},
		{PID: 10, At: now.Add(-1 * time.Minute), OldGenPct: 90},
	}
	got := h.SamplesFor(10)
	if len(got) != 3 {
		t.Fatalf("expected 3 samples for PID 10, got %d", len(got))
	}
	for i, want := range []float64{70, 80, 90} {
		if got[i].OldGenPct != want {
			t.Errorf("sample %d: OldGenPct = %v, want %v", i, got[i].OldGenPct, want)
		}
	}
}

func TestJVMHistory_SamplesForNoMatch(t *testing.T) {
	h := JVMHistory{{PID: 10, OldGenPct: 50}}
	if got := h.SamplesFor(99); got != nil {
		t.Errorf("expected nil for missing PID, got %v", got)
	}
}

func TestJVMHistory_SamplesForEmpty(t *testing.T) {
	var h JVMHistory
	if got := h.SamplesFor(10); got != nil {
		t.Errorf("expected nil from empty history, got %v", got)
	}
}
