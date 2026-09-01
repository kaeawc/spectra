// Package storagegrowth diffs the storage sections of two snapshots to find
// what grew over time, and how fast (bytes/day). It reads only.
package storagegrowth

import (
	"sort"
	"time"

	"github.com/kaeawc/spectra/internal/storagestate"
)

// Delta is the change in one storage area between two captures.
type Delta struct {
	Category    string  `json:"category"` // volume | user-library | app-caches | app
	Label       string  `json:"label"`    // mount point, app path, or a fixed name
	BeforeBytes int64   `json:"before_bytes"`
	AfterBytes  int64   `json:"after_bytes"`
	GrowthBytes int64   `json:"growth_bytes"`
	BytesPerDay float64 `json:"bytes_per_day"`
}

// Report ranks the storage areas that grew between two captures.
type Report struct {
	BeforeAt         time.Time `json:"before_at"`
	AfterAt          time.Time `json:"after_at"`
	Days             float64   `json:"days"`
	TotalGrowthBytes int64     `json:"total_growth_bytes"`
	Deltas           []Delta   `json:"deltas"`
	Note             string    `json:"note,omitempty"`
}

// Compute diffs two storage states and ranks growth descending, keeping the top
// N growers (topN <= 0 keeps all growers). Shrinking areas are excluded from
// the ranking but still count toward TotalGrowthBytes.
func Compute(before, after storagestate.State, beforeAt, afterAt time.Time, topN int) Report {
	rep := Report{BeforeAt: beforeAt, AfterAt: afterAt}
	rep.Days = afterAt.Sub(beforeAt).Hours() / 24
	if rep.Days <= 0 {
		rep.Note = "non-positive interval between snapshots; rates are zero"
	}

	var deltas []Delta
	add := func(cat, label string, b, a int64) {
		g := a - b
		rep.TotalGrowthBytes += g
		deltas = append(deltas, Delta{
			Category: cat, Label: label,
			BeforeBytes: b, AfterBytes: a, GrowthBytes: g,
			BytesPerDay: perDay(g, rep.Days),
		})
	}

	beforeVols := indexVolumes(before.Volumes)
	for _, v := range after.Volumes {
		add("volume", v.MountPoint, beforeVols[v.MountPoint], v.UsedBytes)
	}
	add("user-library", "~/Library", before.UserLibraryBytes, after.UserLibraryBytes)
	add("app-caches", "~/Library/Caches", before.AppCachesBytes, after.AppCachesBytes)

	beforeApps := indexApps(before.LargestApps)
	for _, app := range after.LargestApps {
		add("app", app.Path, beforeApps[app.Path], app.OnDiskBytes)
	}

	// Rank only growers, largest first.
	growers := make([]Delta, 0, len(deltas))
	for _, d := range deltas {
		if d.GrowthBytes > 0 {
			growers = append(growers, d)
		}
	}
	sort.SliceStable(growers, func(i, j int) bool {
		if growers[i].GrowthBytes != growers[j].GrowthBytes {
			return growers[i].GrowthBytes > growers[j].GrowthBytes
		}
		return growers[i].Label < growers[j].Label
	})
	if topN > 0 && len(growers) > topN {
		growers = growers[:topN]
	}
	rep.Deltas = growers
	return rep
}

func perDay(growth int64, days float64) float64 {
	if days <= 0 {
		return 0
	}
	return float64(growth) / days
}

func indexVolumes(vols []storagestate.Volume) map[string]int64 {
	m := make(map[string]int64, len(vols))
	for _, v := range vols {
		m[v.MountPoint] = v.UsedBytes
	}
	return m
}

func indexApps(apps []storagestate.AppSize) map[string]int64 {
	m := make(map[string]int64, len(apps))
	for _, a := range apps {
		m[a.Path] = a.OnDiskBytes
	}
	return m
}
