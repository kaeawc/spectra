package rules

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// helper: build N samples for one PID, with linearly interpolated OldGenPct.
func samples(pid int, n int, startPct, endPct float64) snapshot.JVMHistory {
	out := make(snapshot.JVMHistory, n)
	step := 0.0
	if n > 1 {
		step = (endPct - startPct) / float64(n-1)
	}
	now := time.Now()
	for i := 0; i < n; i++ {
		out[i] = snapshot.JVMSample{
			PID:       pid,
			At:        now.Add(time.Duration(i-n) * time.Minute),
			OldGenPct: startPct + step*float64(i),
		}
	}
	return out
}

func TestRisingOldGenFor_NoHistory(t *testing.T) {
	if RisingOldGenFor(nil, 10) {
		t.Error("nil history should not report rising")
	}
}

func TestRisingOldGenFor_TooFewSamples(t *testing.T) {
	h := samples(10, 2, 50, 95) // huge delta but only 2 samples
	if RisingOldGenFor(h, 10) {
		t.Error("only 2 samples should not be enough for trend")
	}
}

func TestRisingOldGenFor_RisingTrend(t *testing.T) {
	h := samples(10, 5, 70, 95) // 25pp rise across 5 samples
	if !RisingOldGenFor(h, 10) {
		t.Error("70 -> 95 across 5 samples should be rising")
	}
}

func TestRisingOldGenFor_FlatTrend(t *testing.T) {
	h := samples(10, 5, 95, 96) // flat near ceiling
	if RisingOldGenFor(h, 10) {
		t.Error("95 -> 96 should not be rising (below MinOldGenRiseDelta)")
	}
}

func TestRisingOldGenFor_FallingTrend(t *testing.T) {
	h := samples(10, 5, 95, 70) // falling
	if RisingOldGenFor(h, 10) {
		t.Error("95 -> 70 should not be rising")
	}
}

func TestRisingOldGenFor_OtherPID(t *testing.T) {
	h := samples(10, 5, 70, 95)
	if RisingOldGenFor(h, 99) {
		t.Error("trend for another PID should not match")
	}
}

func TestHasTrendFor(t *testing.T) {
	if HasTrendFor(nil, 10) {
		t.Error("nil history → no trend")
	}
	if HasTrendFor(samples(10, 2, 50, 60), 10) {
		t.Error("2 samples → not enough")
	}
	if !HasTrendFor(samples(10, 3, 50, 60), 10) {
		t.Error("3 samples → enough")
	}
}

// helper: build fd samples for one PID from an explicit sequence of counts.
func fdSamples(pid int, counts ...int) snapshot.FDHistory {
	out := make(snapshot.FDHistory, len(counts))
	now := time.Now()
	for i, c := range counts {
		out[i] = snapshot.FDSample{
			PID:     pid,
			At:      now.Add(time.Duration(i-len(counts)) * time.Minute),
			OpenFDs: c,
		}
	}
	return out
}

func TestRisingFDsFor_NoHistory(t *testing.T) {
	if RisingFDsFor(nil, 10) {
		t.Error("nil history should not report rising")
	}
}

func TestRisingFDsFor_TooFewSamples(t *testing.T) {
	if RisingFDsFor(fdSamples(10, 100, 500), 10) { // big jump but only 2 samples
		t.Error("only 2 samples should not be enough for trend")
	}
}

func TestRisingFDsFor_RisingTrend(t *testing.T) {
	if !RisingFDsFor(fdSamples(10, 100, 150, 220), 10) {
		t.Error("100 -> 150 -> 220 should be rising")
	}
}

func TestRisingFDsFor_FlatTrend(t *testing.T) {
	if RisingFDsFor(fdSamples(10, 200, 200, 200), 10) {
		t.Error("flat window should not be rising (no net climb)")
	}
}

func TestRisingFDsFor_NonMonotonicNotRising(t *testing.T) {
	// Net climb from first to last, but a mid-window dip disqualifies it.
	if RisingFDsFor(fdSamples(10, 100, 90, 130), 10) {
		t.Error("a mid-window dip should disqualify the rising trend")
	}
}

func TestRisingFDsFor_FallingTrend(t *testing.T) {
	if RisingFDsFor(fdSamples(10, 300, 200, 100), 10) {
		t.Error("300 -> 100 should not be rising")
	}
}

func TestRisingFDsFor_OtherPID(t *testing.T) {
	if RisingFDsFor(fdSamples(10, 100, 150, 220), 99) {
		t.Error("trend for another PID should not match")
	}
}

func TestHasFDTrendFor(t *testing.T) {
	if HasFDTrendFor(nil, 10) {
		t.Error("nil history → no trend")
	}
	if HasFDTrendFor(fdSamples(10, 100, 200), 10) {
		t.Error("2 samples → not enough")
	}
	if !HasFDTrendFor(fdSamples(10, 100, 150, 200), 10) {
		t.Error("3 samples → enough")
	}
}
