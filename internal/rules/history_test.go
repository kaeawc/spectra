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
