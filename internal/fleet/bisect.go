package fleet

import (
	"time"

	"github.com/kaeawc/spectra/internal/diff"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
)

// CoChange is one change that co-occurred in the snapshot where a symptom first
// appeared. It is correlational context, not an identified cause.
type CoChange struct {
	Section string `json:"section"`
	Kind    string `json:"kind"`
	Key     string `json:"key"`
	Before  string `json:"before,omitempty"`
	After   string `json:"after,omitempty"`
}

// BisectResult is the outcome of a symptom bisection over a snapshot series.
type BisectResult struct {
	Status       string     `json:"status"` // clean, already-firing, found
	RuleID       string     `json:"rule_id"`
	Snapshots    int        `json:"snapshots"`
	FirstBadID   string     `json:"first_bad_id,omitempty"`
	FirstBadTime time.Time  `json:"first_bad_time,omitempty"`
	PrevID       string     `json:"prev_id,omitempty"`
	Changes      []CoChange `json:"co_occurring_changes,omitempty"`
}

// BisectSymptom walks an oldest→newest snapshot series and finds the first
// snapshot where ruleID starts firing, then (when there is a predecessor) the
// changes that co-occurred in that snapshot. Because snapshots are periodic,
// the result is bounded by capture cadence, and the co-occurring changes are
// correlational only.
func BisectSymptom(series []snapshot.Snapshot, ruleID string, catalog []rules.Rule) BisectResult {
	r := BisectResult{RuleID: ruleID, Snapshots: len(series), Status: "clean"}
	firstBad := -1
	for i, s := range series {
		if ruleFires(s, ruleID, catalog) {
			firstBad = i
			break
		}
	}
	if firstBad < 0 {
		return r // rule never fires across the series
	}
	r.FirstBadID = series[firstBad].ID
	r.FirstBadTime = series[firstBad].TakenAt
	if firstBad == 0 {
		r.Status = "already-firing" // firing in the oldest snapshot; nothing earlier to compare
		return r
	}
	r.Status = "found"
	prev := series[firstBad-1]
	r.PrevID = prev.ID
	for _, sec := range diff.Compare(prev, series[firstBad]).Sections {
		for _, c := range sec.Changes {
			r.Changes = append(r.Changes, CoChange{
				Section: sec.Name,
				Kind:    string(c.Kind),
				Key:     c.Key,
				Before:  c.Before,
				After:   c.After,
			})
		}
	}
	return r
}
