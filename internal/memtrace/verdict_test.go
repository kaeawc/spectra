package memtrace

import (
	"math"
	"testing"
)

const mib = 1024

// perMinute builds one sample per minute.
func perMinute(rss ...int64) []Sample {
	out := make([]Sample, len(rss))
	for i, r := range rss {
		out[i] = Sample{ElapsedMS: int64(i) * 60000, RSSKiB: r}
	}
	return out
}

func mibs(v ...int64) []int64 {
	out := make([]int64, len(v))
	for i, x := range v {
		out[i] = x * mib
	}
	return out
}

func TestSummarizeVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		rss     []int64
		verdict string
		pattern string
	}{
		{"flat", []int64{102400, 102401, 102400, 102402, 102400, 102401}, VerdictSteady, ""},
		{"linear growth", mibs(100, 105, 110, 115, 120, 125), VerdictGrowing, ""},
		{"linear shrink", mibs(125, 120, 115, 110, 105, 100), VerdictShrinking, ""},
		{"flat sawtooth ending on a peak", mibs(100, 150, 100, 150, 100, 150), VerdictSawtooth, ""},
		{"flat sawtooth ending on a trough", mibs(100, 150, 100, 150, 100, 150, 100), VerdictSawtooth, ""},
		{"leak under sawtooth ending on a trough", mibs(100, 130, 104, 134, 108, 138, 112, 142, 116, 146, 120), VerdictGrowing, PatternSawtooth},
		{"leak under sawtooth ending on a peak", mibs(100, 130, 104, 134, 108, 138, 112, 142, 116, 146, 120, 150), VerdictGrowing, PatternSawtooth},
		{"drain under sawtooth", mibs(146, 116, 142, 112, 138, 108, 134, 104, 130, 100, 126), VerdictShrinking, PatternSawtooth},
		{"single spike in a flat series", mibs(100, 100, 100, 100, 100, 200, 100, 100, 100, 100), VerdictSteady, ""},
		{"last-sample outlier", mibs(100, 100, 100, 100, 100, 100, 100, 100, 100, 150), VerdictSteady, ""},
		// +48 KiB is 24% of this process, but under the 1 MiB floor.
		{"tiny process +48 KiB", []int64{200, 212, 224, 236, 248}, VerdictSteady, ""},
		{"all zeros", []int64{0, 0, 0, 0, 0, 0}, VerdictUnreadable, ""},
		{"all zeros, few samples", []int64{0, 0}, VerdictUnreadable, ""},
		{"4 samples", mibs(100, 200, 300, 400), VerdictInsufficient, ""},
		{"no samples", nil, VerdictInsufficient, ""},
		// The verdict describes the window: memory rose by half and then
		// held, so the median pairwise slope is still clearly positive.
		{"growth then plateau", mibs(100, 110, 120, 130, 140, 150, 150, 150, 150, 150), VerdictGrowing, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := Summarize(perMinute(tc.rss...))
			if s.Verdict != tc.verdict || s.Pattern != tc.pattern {
				t.Fatalf("verdict = %q pattern = %q, want %q %q (summary %+v)", s.Verdict, s.Pattern, tc.verdict, tc.pattern, s)
			}
		})
	}
}

func TestSummarizeKeepsEndpointFacts(t *testing.T) {
	s := Summarize(perMinute(mibs(100, 150, 100, 150, 100, 150)...))
	if s.FirstKiB != 100*mib || s.LastKiB != 150*mib || s.NetChangeKiB != 50*mib || s.MinKiB != 100*mib || s.MaxKiB != 150*mib {
		t.Fatalf("summary = %+v", s)
	}
}

func TestUnevenlySpacedSamples(t *testing.T) {
	// 6 MiB per minute at irregular times.
	at := []int64{0, 10000, 70000, 75000, 200000, 300000}
	var ss []Sample
	for _, ms := range at {
		ss = append(ss, Sample{ElapsedMS: ms, RSSKiB: 100*mib + 6*mib*ms/60000})
	}
	s := Summarize(ss)
	if s.Verdict != VerdictGrowing || math.Abs(s.GrowthKiBPerMin-6*mib) > 1 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestSubtreeVerdict(t *testing.T) {
	ss := perMinute(mibs(100, 100, 100, 100, 100)...)
	for i := range ss {
		ss[i].Processes, ss[i].SubtreeRSSKiB = 2, int64(200+10*i)*mib
	}
	s := Summarize(ss)
	if s.Verdict != VerdictSteady || s.SubtreeVerdict != VerdictGrowing || math.Abs(s.SubtreeGrowthMin-10*mib) > 1e-6 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFlatSlopeIsExactZero(t *testing.T) {
	s := Summarize(perMinute(mibs(100, 100, 100, 100, 100)...))
	if s.GrowthKiBPerMin != 0 || math.Signbit(s.GrowthKiBPerMin) {
		t.Fatalf("growth = %v", s.GrowthKiBPerMin)
	}
	for _, v := range []float64{-6.5e-12, 0.049, -0.049, math.Copysign(0, -1), math.NaN(), math.Inf(1)} {
		if got := snap(v); got != 0 || math.Signbit(got) {
			t.Errorf("snap(%v) = %v", v, got)
		}
	}
	if snap(-0.05) != -0.05 {
		t.Error("snap should keep slopes at the threshold")
	}
}

func TestTurningPoints(t *testing.T) {
	pts := pointsOf(perMinute(100, 200, 100, 200, 100), func(s Sample) int64 { return s.RSSKiB })
	if got := turningPoints(pts, 10); len(got) != 3 || !got[0].max || got[1].max {
		t.Fatalf("turns = %+v", got)
	}
	// A single spike reverses once: one turning point, not an oscillation.
	pts = pointsOf(perMinute(100, 100, 300, 100, 100), func(s Sample) int64 { return s.RSSKiB })
	if got := turningPoints(pts, 10); len(got) != 1 {
		t.Fatalf("turns = %+v", got)
	}
	// Swings below the threshold are not reversals.
	pts = pointsOf(perMinute(100, 105, 100, 105, 100, 105), func(s Sample) int64 { return s.RSSKiB })
	if got := turningPoints(pts, 10); len(got) != 0 {
		t.Fatalf("turns = %+v", got)
	}
}

func TestTheilSenDownsamples(t *testing.T) {
	pts := make([]point, 5000)
	for i := range pts {
		pts[i] = point{x: float64(i), y: 3 * float64(i)}
	}
	if got := theilSen(pts); math.Abs(got-3) > 1e-9 {
		t.Fatalf("slope = %v", got)
	}
	if len(downsample(pts, maxFit)) != maxFit || downsample(pts, maxFit)[maxFit-1] != pts[len(pts)-1] {
		t.Fatal("downsample should keep both ends")
	}
}
