package memtrace

import (
	"math"
	"sort"
)

const (
	changeFrac  = 0.05   // projected trend change, relative to the median, that counts as growth or shrinkage
	floorKiB    = 1024.0 // absolute threshold floor so page-level noise in tiny processes is not growth
	swingFrac   = 0.10   // swing between turning points, relative to the median, that counts as a reversal
	minTurns    = 4      // turning points that make two full cycles
	minTroughs  = 3      // troughs needed to fit a trend under a sawtooth
	maxFit      = 2000   // samples kept for the O(n²) Theil–Sen fit
	snapKiBMin  = 0.05   // slopes smaller than this are reported as exactly zero
	msPerMinute = 60000.0
)

// PatternSawtooth marks a growing or shrinking verdict whose series also
// oscillates, so the trend was fitted over the troughs.
const PatternSawtooth = "sawtooth"

type point struct{ x, y float64 }

type turn struct {
	point
	max bool
}

// trend is the verdict for one series.
type trend struct {
	verdict string
	pattern string
	// slope is the Theil–Sen slope over all samples.
	slope float64
}

func pointsOf(samples []Sample, val func(Sample) int64) []point {
	pts := make([]point, len(samples))
	for i, s := range samples {
		pts[i] = point{x: float64(s.ElapsedMS) / msPerMinute, y: float64(val(s))}
	}
	return pts
}

// classify gives the verdict for a series of (minutes, KiB) points. It never
// looks at the endpoints alone: the trend is the Theil–Sen slope projected
// over the observed span, and an oscillating series is judged by its troughs.
func classify(pts []point) trend {
	if len(pts) == 0 {
		return trend{verdict: VerdictInsufficient}
	}
	if allZero(pts) {
		return trend{verdict: VerdictUnreadable}
	}
	slope := theilSen(pts)
	tr := trend{verdict: VerdictInsufficient, slope: snap(slope)}
	if len(pts) < MinSamples {
		return tr
	}
	med := median(ys(pts))
	thr := max(changeFrac*med, floorKiB)
	span := pts[len(pts)-1].x - pts[0].x
	turns := turningPoints(pts, swingFrac*med)
	if len(turns) < minTurns {
		tr.verdict = direction(slope*span, thr, VerdictSteady)
		return tr
	}
	tr.verdict = VerdictSawtooth
	v := sawtoothDirection(turns, slope*span, span, thr)
	if v != VerdictSawtooth {
		tr.verdict, tr.pattern = v, PatternSawtooth
	}
	return tr
}

// sawtoothDirection judges an oscillating series. With enough troughs they
// are fitted; with fewer, the all-sample projection decides, and two troughs
// must also agree with it in sign (as must the peaks, when there are two).
func sawtoothDirection(turns []turn, allProj, span, thr float64) string {
	troughs := minima(turns)
	if len(troughs) >= minTroughs {
		return direction(theilSen(troughs)*span, thr, VerdictSawtooth)
	}
	v := direction(allProj, thr, VerdictSawtooth)
	if v == VerdictSawtooth || len(troughs) != 2 {
		return v
	}
	peaks := maxima(turns)
	if len(peaks) < 2 {
		return VerdictSawtooth
	}
	if sameSign(troughs[1].y-troughs[0].y, allProj) && sameSign(peaks[len(peaks)-1].y-peaks[0].y, allProj) {
		return v
	}
	return VerdictSawtooth
}

func sameSign(a, b float64) bool { return a*b > 0 }

func direction(projected, thr float64, flat string) string {
	switch {
	case projected >= thr:
		return VerdictGrowing
	case projected <= -thr:
		return VerdictShrinking
	}
	return flat
}

func allZero(pts []point) bool {
	for _, p := range pts {
		if p.y != 0 {
			return false
		}
	}
	return true
}

func ys(pts []point) []float64 {
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = p.y
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// theilSen is the median of all pairwise slopes; it ignores up to ~29% of
// outliers, so one spike or a misplaced endpoint does not move it.
func theilSen(pts []point) float64 {
	pts = downsample(pts, maxFit)
	var slopes []float64
	for i := range pts {
		for j := i + 1; j < len(pts); j++ {
			if dx := pts[j].x - pts[i].x; dx != 0 {
				slopes = append(slopes, (pts[j].y-pts[i].y)/dx)
			}
		}
	}
	return median(slopes)
}

func downsample(pts []point, n int) []point {
	if len(pts) <= n {
		return pts
	}
	out := make([]point, n)
	for i := range out {
		out[i] = pts[i*(len(pts)-1)/(n-1)]
	}
	return out
}

// snap reports float cancellation noise as zero and keeps NaN/Inf out of JSON.
func snap(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) < snapKiBMin {
		return 0
	}
	return v
}

// zigzag confirms a turning point only once the series has reversed from it
// by at least swing, so small wiggles and a lone trailing extreme are ignored.
type zigzag struct {
	swing  float64
	dir    int // 0 until the first move of swing, then +1 rising or -1 falling
	lo, hi point
	cand   point
	turns  []turn
}

func turningPoints(pts []point, swing float64) []turn {
	if len(pts) == 0 || swing <= 0 {
		return nil
	}
	z := &zigzag{swing: swing, lo: pts[0], hi: pts[0]}
	for _, p := range pts[1:] {
		z.step(p)
	}
	return z.turns
}

func (z *zigzag) step(p point) {
	switch z.dir {
	case 0:
		z.start(p)
	case 1:
		if p.y >= z.cand.y {
			z.cand = p
		} else if z.cand.y-p.y >= z.swing {
			z.turns = append(z.turns, turn{z.cand, true})
			z.dir, z.cand = -1, p
		}
	default:
		if p.y <= z.cand.y {
			z.cand = p
		} else if p.y-z.cand.y >= z.swing {
			z.turns = append(z.turns, turn{z.cand, false})
			z.dir, z.cand = 1, p
		}
	}
}

// start finds the first move of swing from the opening extremes; the opening
// extreme itself is a trace boundary, not a turning point.
func (z *zigzag) start(p point) {
	switch {
	case p.y-z.lo.y >= z.swing:
		z.dir, z.cand = 1, p
	case z.hi.y-p.y >= z.swing:
		z.dir, z.cand = -1, p
	case p.y < z.lo.y:
		z.lo = p
	case p.y > z.hi.y:
		z.hi = p
	}
}

func minima(turns []turn) []point {
	var out []point
	for _, t := range turns {
		if !t.max {
			out = append(out, t.point)
		}
	}
	return out
}

func maxima(turns []turn) []point {
	var out []point
	for _, t := range turns {
		if t.max {
			out = append(out, t.point)
		}
	}
	return out
}
