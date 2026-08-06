// Package anomaly is a small, dependency-free rolling-baseline detector. It
// builds an exponentially-weighted (EWMA) mean and variance for each series and
// flags the *latest* point when it sits more than a z-score threshold away from
// that baseline — i.e. a sudden jump or step change relative to the recent
// history. It does not track a gradual trend: a slow creep that the EWMA
// baseline follows will not be flagged; that is what a dedicated trend detector
// would add.
package anomaly

import (
	"math"
	"sort"
	"time"
)

// Point is one observation in a series.
type Point struct {
	Value float64
	At    time.Time
}

// Series is a named sequence of observations (any order; sorted internally).
type Series struct {
	Key    string
	Points []Point
}

// Finding describes a series whose latest value is anomalous vs its baseline.
type Finding struct {
	Key     string  `json:"key"`
	Latest  float64 `json:"latest"`
	Mean    float64 `json:"baseline_mean"`
	StdDev  float64 `json:"baseline_stddev"`
	Z       float64 `json:"z"`
	Samples int     `json:"samples"` // baseline sample count (excludes the latest point)
}

// defaultAlpha is the EWMA smoothing factor: higher weights recent points more.
const defaultAlpha = 0.3

// Detect flags each series whose latest point deviates from its EWMA baseline by
// more than zThreshold standard deviations. A series needs at least minSamples
// baseline points (cold-start guard) and non-zero baseline variance. Results are
// sorted by descending absolute z-score.
func Detect(series []Series, minSamples int, zThreshold float64) []Finding {
	var out []Finding
	for _, s := range series {
		if f, ok := detectOne(s, minSamples, zThreshold); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return math.Abs(out[i].Z) > math.Abs(out[j].Z) })
	return out
}

func detectOne(s Series, minSamples int, zThreshold float64) (Finding, bool) {
	pts := append([]Point(nil), s.Points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].At.Before(pts[j].At) })
	if len(pts) < minSamples+1 {
		return Finding{}, false // cold start: not enough baseline yet
	}
	// EWMA mean + variance over every point except the latest (the baseline).
	mean := pts[0].Value
	variance := 0.0
	for i := 1; i < len(pts)-1; i++ {
		x := pts[i].Value
		diff := x - mean
		mean += defaultAlpha * diff
		variance = (1 - defaultAlpha) * (variance + defaultAlpha*diff*diff)
	}
	std := math.Sqrt(variance)
	if std <= 0 {
		return Finding{}, false // flat baseline: no meaningful z-score
	}
	latest := pts[len(pts)-1].Value
	z := (latest - mean) / std
	if math.Abs(z) < zThreshold {
		return Finding{}, false
	}
	return Finding{Key: s.Key, Latest: latest, Mean: mean, StdDev: std, Z: z, Samples: len(pts) - 1}, true
}
