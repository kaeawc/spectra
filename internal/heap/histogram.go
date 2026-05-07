// Package heap contains reusable app heap-analysis building blocks.
package heap

import (
	"fmt"
	"strconv"
	"strings"
)

// ClassEntry is one row from a JVM class histogram.
type ClassEntry struct {
	Rank      int    `json:"rank"`
	Instances int64  `json:"instances"`
	Bytes     int64  `json:"bytes"`
	ClassName string `json:"class_name"`
	Module    string `json:"module,omitempty"`
}

// Identity returns the stable record key used by generic heap analysis.
func (e ClassEntry) Identity() string {
	if e.Module == "" {
		return e.ClassName
	}
	return e.Module + ":" + e.ClassName
}

// DisplayName returns the human-readable record name.
func (e ClassEntry) DisplayName() string {
	return e.ClassName
}

// LiveCount returns the live instance count for this class.
func (e ClassEntry) LiveCount() int64 {
	return e.Instances
}

// ShallowBytes returns the shallow bytes for this class.
func (e ClassEntry) ShallowBytes() int64 {
	return e.Bytes
}

// Histogram is a structured view of jcmd GC.class_histogram output.
type Histogram struct {
	Entries []ClassEntry `json:"entries"`
	Total   ClassEntry   `json:"total"`
	Skipped []string     `json:"skipped,omitempty"`
}

// Runtime returns the source runtime for this heap snapshot.
func (Histogram) Runtime() Runtime {
	return RuntimeJVM
}

// Source returns the collector output that produced this snapshot.
func (Histogram) Source() string {
	return "jcmd GC.class_histogram"
}

// Records returns generic heap records for runtime-neutral analysis.
func (h Histogram) Records() []Record {
	records := make([]Record, 0, len(h.Entries))
	for i := range h.Entries {
		records = append(records, h.Entries[i])
	}
	return records
}

// TotalShallowBytes returns the total shallow bytes reported by the collector.
func (h Histogram) TotalShallowBytes() int64 {
	return h.Total.Bytes
}

// JVMHistogramParser parses jcmd GC.class_histogram output.
type JVMHistogramParser struct{}

// Runtime returns the parser's supported runtime.
func (JVMHistogramParser) Runtime() Runtime {
	return RuntimeJVM
}

// ParseSnapshot parses a JVM class histogram into a generic heap snapshot.
func (JVMHistogramParser) ParseSnapshot(out []byte) (Snapshot, error) {
	return ParseHistogram(string(out))
}

// ParseHistogram parses the text emitted by jcmd GC.class_histogram.
func ParseHistogram(out string) (Histogram, error) {
	var h Histogram
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "num ") || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "Total" {
			instances, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return h, fmt.Errorf("parse histogram total instances: %w", err)
			}
			bytes, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return h, fmt.Errorf("parse histogram total bytes: %w", err)
			}
			h.Total = ClassEntry{Instances: instances, Bytes: bytes, ClassName: "Total"}
			continue
		}
		if len(fields) < 4 {
			h.Skipped = append(h.Skipped, raw)
			continue
		}
		rankText := strings.TrimSuffix(fields[0], ":")
		rank, err := strconv.Atoi(rankText)
		if err != nil {
			h.Skipped = append(h.Skipped, raw)
			continue
		}
		instances, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return h, fmt.Errorf("parse histogram instances on rank %d: %w", rank, err)
		}
		bytes, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return h, fmt.Errorf("parse histogram bytes on rank %d: %w", rank, err)
		}
		name, module := splitClassAndModule(strings.Join(fields[3:], " "))
		entry := ClassEntry{
			Rank:      rank,
			Instances: instances,
			Bytes:     bytes,
			ClassName: name,
			Module:    module,
		}
		h.Entries = append(h.Entries, entry)
	}
	return h, nil
}

func splitClassAndModule(s string) (className, module string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		return strings.TrimSpace(s[:i]), strings.TrimSuffix(s[i+2:], ")")
	}
	return s, ""
}

// HistogramDelta is the class-level difference between two histograms.
type HistogramDelta struct {
	ClassName    string `json:"class_name"`
	BeforeCount  int64  `json:"before_count"`
	AfterCount   int64  `json:"after_count"`
	DeltaCount   int64  `json:"delta_count"`
	BeforeBytes  int64  `json:"before_bytes"`
	AfterBytes   int64  `json:"after_bytes"`
	DeltaBytes   int64  `json:"delta_bytes"`
	BeforeRank   int    `json:"before_rank,omitempty"`
	AfterRank    int    `json:"after_rank,omitempty"`
	BeforeModule string `json:"before_module,omitempty"`
	AfterModule  string `json:"after_module,omitempty"`
}

// CompareHistograms computes class-level changes from before to after.
func CompareHistograms(before, after Histogram) []HistogramDelta {
	generic, err := DefaultAnalyzer{}.Compare(before, after)
	if err != nil {
		return nil
	}
	deltas := make([]HistogramDelta, 0, len(generic))
	beforeByID := histogramEntriesByID(before)
	afterByID := histogramEntriesByID(after)
	for _, gd := range generic {
		d := HistogramDelta{
			ClassName:   gd.DisplayName,
			BeforeCount: gd.BeforeCount,
			AfterCount:  gd.AfterCount,
			DeltaCount:  gd.DeltaCount,
			BeforeBytes: gd.BeforeBytes,
			AfterBytes:  gd.AfterBytes,
			DeltaBytes:  gd.DeltaBytes,
		}
		if e, ok := beforeByID[gd.Identity]; ok {
			d.BeforeRank = e.Rank
			d.BeforeModule = e.Module
		}
		if e, ok := afterByID[gd.Identity]; ok {
			d.AfterRank = e.Rank
			d.AfterModule = e.Module
		}
		deltas = append(deltas, d)
	}
	return deltas
}

func histogramEntriesByID(h Histogram) map[string]ClassEntry {
	entries := make(map[string]ClassEntry, len(h.Entries))
	for _, e := range h.Entries {
		entries[e.Identity()] = e
	}
	return entries
}

// Suspect describes a class worth inspecting in a heap-forensics workflow.
type Suspect struct {
	ClassName  string `json:"class_name"`
	Reason     string `json:"reason"`
	Instances  int64  `json:"instances"`
	Bytes      int64  `json:"bytes"`
	DeltaCount int64  `json:"delta_count,omitempty"`
	DeltaBytes int64  `json:"delta_bytes,omitempty"`
}

// RankHistogramSuspects returns the largest classes in one histogram.
func RankHistogramSuspects(h Histogram, limit int) []Suspect {
	generic := DefaultAnalyzer{}.RankLargest(h, limit)
	suspects := make([]Suspect, 0, len(generic))
	for _, s := range generic {
		suspects = append(suspects, Suspect{
			ClassName: s.Name,
			Reason:    "largest live shallow size in class histogram",
			Instances: s.Count,
			Bytes:     s.Bytes,
		})
	}
	return suspects
}

// RankGrowthSuspects returns the largest positive class growth between two histograms.
func RankGrowthSuspects(before, after Histogram, limit int) []Suspect {
	generic, err := DefaultAnalyzer{}.RankGrowth(before, after, limit)
	if err != nil {
		return nil
	}
	suspects := make([]Suspect, 0, len(generic))
	for _, s := range generic {
		suspects = append(suspects, Suspect{
			ClassName:  s.Name,
			Reason:     "largest positive shallow-size growth between histograms",
			Instances:  s.Count,
			Bytes:      s.Bytes,
			DeltaCount: s.DeltaCount,
			DeltaBytes: s.DeltaBytes,
		})
	}
	return suspects
}
