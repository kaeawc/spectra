package heap

import (
	"fmt"
	"sort"
)

// Runtime identifies the application/runtime architecture that produced heap
// evidence. New collectors should add runtime-specific adapters that return
// generic heap records through this package's interfaces.
type Runtime string

const (
	RuntimeUnknown Runtime = "unknown"
	RuntimeJVM     Runtime = "jvm"
	RuntimeNative  Runtime = "native"
	RuntimeNode    Runtime = "node"
	RuntimePython  Runtime = "python"
	RuntimeWebKit  Runtime = "webkit"
)

// Record is the smallest common shape shared by heap-like object summaries.
// JVM classes, native allocation sites, JavaScript constructors, Python types,
// and WebKit object groups can all map onto this interface.
type Record interface {
	Identity() string
	DisplayName() string
	LiveCount() int64
	ShallowBytes() int64
}

// Snapshot is a runtime-neutral heap observation.
type Snapshot interface {
	Runtime() Runtime
	Source() string
	Records() []Record
	TotalShallowBytes() int64
}

// Parser turns runtime-specific heap evidence into a generic snapshot.
type Parser interface {
	Runtime() Runtime
	ParseSnapshot([]byte) (Snapshot, error)
}

// Analyzer is the reusable heap-analysis contract for all app runtimes.
type Analyzer interface {
	Compare(before, after Snapshot) ([]Delta, error)
	RankLargest(snapshot Snapshot, limit int) []AnalysisSuspect
	RankGrowth(before, after Snapshot, limit int) ([]AnalysisSuspect, error)
}

// DefaultAnalyzer implements runtime-neutral shallow-size analysis.
type DefaultAnalyzer struct{}

// Delta is one record-level difference between two generic heap snapshots.
type Delta struct {
	Identity    string  `json:"identity"`
	DisplayName string  `json:"display_name"`
	Runtime     Runtime `json:"runtime"`
	BeforeCount int64   `json:"before_count"`
	AfterCount  int64   `json:"after_count"`
	DeltaCount  int64   `json:"delta_count"`
	BeforeBytes int64   `json:"before_bytes"`
	AfterBytes  int64   `json:"after_bytes"`
	DeltaBytes  int64   `json:"delta_bytes"`
}

// AnalysisSuspect describes a runtime-neutral record worth inspecting.
type AnalysisSuspect struct {
	Identity   string  `json:"identity"`
	Name       string  `json:"name"`
	Runtime    Runtime `json:"runtime"`
	Reason     string  `json:"reason"`
	Count      int64   `json:"count"`
	Bytes      int64   `json:"bytes"`
	DeltaCount int64   `json:"delta_count,omitempty"`
	DeltaBytes int64   `json:"delta_bytes,omitempty"`
}

// Compare computes record-level changes from before to after.
func (DefaultAnalyzer) Compare(before, after Snapshot) ([]Delta, error) {
	if before == nil || after == nil {
		return nil, fmt.Errorf("compare heap snapshots: nil snapshot")
	}
	type pair struct {
		before Record
		after  Record
	}
	byID := make(map[string]pair, len(before.Records())+len(after.Records()))
	for _, r := range before.Records() {
		p := byID[r.Identity()]
		p.before = r
		byID[r.Identity()] = p
	}
	for _, r := range after.Records() {
		p := byID[r.Identity()]
		p.after = r
		byID[r.Identity()] = p
	}

	deltas := make([]Delta, 0, len(byID))
	for id, p := range byID {
		d := Delta{Identity: id, Runtime: after.Runtime()}
		if p.before != nil {
			d.DisplayName = p.before.DisplayName()
			d.BeforeCount = p.before.LiveCount()
			d.BeforeBytes = p.before.ShallowBytes()
		}
		if p.after != nil {
			d.DisplayName = p.after.DisplayName()
			d.AfterCount = p.after.LiveCount()
			d.AfterBytes = p.after.ShallowBytes()
		}
		d.DeltaCount = d.AfterCount - d.BeforeCount
		d.DeltaBytes = d.AfterBytes - d.BeforeBytes
		deltas = append(deltas, d)
	}
	sortDeltas(deltas)
	return deltas, nil
}

// RankLargest returns the largest shallow-size records in one snapshot.
func (DefaultAnalyzer) RankLargest(snapshot Snapshot, limit int) []AnalysisSuspect {
	if snapshot == nil || limit <= 0 {
		return nil
	}
	records := append([]Record(nil), snapshot.Records()...)
	sort.Slice(records, func(i, j int) bool {
		if records[i].ShallowBytes() != records[j].ShallowBytes() {
			return records[i].ShallowBytes() > records[j].ShallowBytes()
		}
		return records[i].Identity() < records[j].Identity()
	})
	if len(records) > limit {
		records = records[:limit]
	}
	suspects := make([]AnalysisSuspect, 0, len(records))
	for _, r := range records {
		suspects = append(suspects, AnalysisSuspect{
			Identity: r.Identity(),
			Name:     r.DisplayName(),
			Runtime:  snapshot.Runtime(),
			Reason:   "largest live shallow size in heap snapshot",
			Count:    r.LiveCount(),
			Bytes:    r.ShallowBytes(),
		})
	}
	return suspects
}

// RankGrowth returns the largest positive shallow-size growth between snapshots.
func (a DefaultAnalyzer) RankGrowth(before, after Snapshot, limit int) ([]AnalysisSuspect, error) {
	if limit <= 0 {
		return nil, nil
	}
	deltas, err := a.Compare(before, after)
	if err != nil {
		return nil, err
	}
	suspects := make([]AnalysisSuspect, 0, limit)
	for _, d := range deltas {
		if d.DeltaBytes <= 0 {
			continue
		}
		suspects = append(suspects, AnalysisSuspect{
			Identity:   d.Identity,
			Name:       d.DisplayName,
			Runtime:    d.Runtime,
			Reason:     "largest positive shallow-size growth between heap snapshots",
			Count:      d.AfterCount,
			Bytes:      d.AfterBytes,
			DeltaCount: d.DeltaCount,
			DeltaBytes: d.DeltaBytes,
		})
		if len(suspects) == limit {
			break
		}
	}
	return suspects, nil
}

func sortDeltas(deltas []Delta) {
	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].DeltaBytes != deltas[j].DeltaBytes {
			return deltas[i].DeltaBytes > deltas[j].DeltaBytes
		}
		if deltas[i].AfterBytes != deltas[j].AfterBytes {
			return deltas[i].AfterBytes > deltas[j].AfterBytes
		}
		return deltas[i].Identity < deltas[j].Identity
	})
}
