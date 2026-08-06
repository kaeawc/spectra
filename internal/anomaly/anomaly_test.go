package anomaly

import (
	"testing"
	"time"
)

func series(key string, values ...float64) Series {
	base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	pts := make([]Point, len(values))
	for i, v := range values {
		pts[i] = Point{Value: v, At: base.Add(time.Duration(i) * time.Minute)}
	}
	return Series{Key: key, Points: pts}
}

func TestDetectSpike(t *testing.T) {
	// noisy ~100 baseline, then a large spike
	s := series("leaky", 98, 102, 99, 101, 100, 103, 97, 1000)
	f := Detect([]Series{s}, 5, 3.0)
	if len(f) != 1 {
		t.Fatalf("findings = %d, want 1: %+v", len(f), f)
	}
	if f[0].Key != "leaky" || f[0].Z < 3 {
		t.Errorf("finding = %+v, want leaky with z>=3", f[0])
	}
	if f[0].Latest != 1000 {
		t.Errorf("latest = %v, want 1000", f[0].Latest)
	}
}

func TestDetectNormalIgnored(t *testing.T) {
	s := series("steady", 98, 102, 99, 101, 100, 103, 97, 100)
	if f := Detect([]Series{s}, 5, 3.0); len(f) != 0 {
		t.Errorf("normal series should not flag, got %+v", f)
	}
}

func TestDetectColdStart(t *testing.T) {
	s := series("young", 100, 5000) // only 2 points, minSamples 5
	if f := Detect([]Series{s}, 5, 3.0); len(f) != 0 {
		t.Errorf("cold-start series should not flag, got %+v", f)
	}
}

func TestDetectFlatBaseline(t *testing.T) {
	// zero-variance baseline can't produce a z-score even with a jump
	s := series("flat", 100, 100, 100, 100, 100, 100, 900)
	if f := Detect([]Series{s}, 5, 3.0); len(f) != 0 {
		t.Errorf("flat baseline should not flag, got %+v", f)
	}
}

func TestDetectSortedByZ(t *testing.T) {
	big := series("big", 98, 102, 99, 101, 100, 103, 97, 5000)
	small := series("small", 98, 102, 99, 101, 100, 103, 97, 400)
	f := Detect([]Series{small, big}, 5, 3.0)
	if len(f) != 2 {
		t.Fatalf("findings = %d, want 2", len(f))
	}
	if f[0].Key != "big" {
		t.Errorf("expected the larger-z 'big' first, got %q", f[0].Key)
	}
}
