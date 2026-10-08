package jvm

import (
	"errors"
	"regexp"
	"sort"
	"strings"
)

var (
	// ErrNMTDisabled means the target JVM was not started with
	// -XX:NativeMemoryTracking=summary|detail.
	ErrNMTDisabled = errors.New("native memory tracking is not enabled")
	// ErrNMTNoBaseline means `summary.diff` ran before `VM.native_memory baseline`.
	ErrNMTNoBaseline = errors.New("no native memory tracking baseline")
)

// NMTCategoryDelta is one category's change between two NMT summaries.
type NMTCategoryDelta struct {
	Name               string `json:"name"`
	BeforeReservedKiB  int64  `json:"before_reserved_kib"`
	AfterReservedKiB   int64  `json:"after_reserved_kib"`
	BeforeCommittedKiB int64  `json:"before_committed_kib"`
	AfterCommittedKiB  int64  `json:"after_committed_kib"`
	DeltaReservedKiB   int64  `json:"delta_reserved_kib"`
	DeltaCommittedKiB  int64  `json:"delta_committed_kib"`
}

// NMTDiff is the change between two NMT summaries, categories sorted by
// committed growth (largest first), then reserved growth.
type NMTDiff struct {
	BeforeReservedKiB  int64              `json:"before_reserved_kib"`
	AfterReservedKiB   int64              `json:"after_reserved_kib"`
	BeforeCommittedKiB int64              `json:"before_committed_kib"`
	AfterCommittedKiB  int64              `json:"after_committed_kib"`
	DeltaReservedKiB   int64              `json:"delta_reserved_kib"`
	DeltaCommittedKiB  int64              `json:"delta_committed_kib"`
	Categories         []NMTCategoryDelta `json:"categories"`
}

// NMTCallSiteDelta is one call site's change between two detail captures.
// Sites are matched by kind plus symbolized stack, so captures from different
// processes (different ASLR bases) still line up.
type NMTCallSiteDelta struct {
	Kind               string   `json:"kind"`
	Category           string   `json:"category,omitempty"`
	Frames             []string `json:"frames"`
	BeforeCommittedKiB int64    `json:"before_committed_kib"`
	AfterCommittedKiB  int64    `json:"after_committed_kib"`
	DeltaReservedKiB   int64    `json:"delta_reserved_kib"`
	DeltaCommittedKiB  int64    `json:"delta_committed_kib"`
}

// DiffNMTSummaries compares two summaries. A category missing on one side is
// treated as zero so new and vanished categories still show up.
func DiffNMTSummaries(before, after NMTBreakdown) NMTDiff {
	d := NMTDiff{
		BeforeReservedKiB:  before.TotalReservedKiB,
		AfterReservedKiB:   after.TotalReservedKiB,
		BeforeCommittedKiB: before.TotalCommittedKiB,
		AfterCommittedKiB:  after.TotalCommittedKiB,
		DeltaReservedKiB:   after.TotalReservedKiB - before.TotalReservedKiB,
		DeltaCommittedKiB:  after.TotalCommittedKiB - before.TotalCommittedKiB,
	}
	byName := map[string]*NMTCategoryDelta{}
	var order []string
	get := func(name string) *NMTCategoryDelta {
		if c, ok := byName[name]; ok {
			return c
		}
		c := &NMTCategoryDelta{Name: name}
		byName[name] = c
		order = append(order, name)
		return c
	}
	for _, c := range before.Categories {
		e := get(c.Name)
		e.BeforeReservedKiB, e.BeforeCommittedKiB = c.ReservedKiB, c.CommittedKiB
	}
	for _, c := range after.Categories {
		e := get(c.Name)
		e.AfterReservedKiB, e.AfterCommittedKiB = c.ReservedKiB, c.CommittedKiB
	}
	for _, name := range order {
		e := byName[name]
		e.DeltaReservedKiB = e.AfterReservedKiB - e.BeforeReservedKiB
		e.DeltaCommittedKiB = e.AfterCommittedKiB - e.BeforeCommittedKiB
		d.Categories = append(d.Categories, *e)
	}
	sortNMTCategoryDeltas(d.Categories)
	return d
}

func sortNMTCategoryDeltas(cs []NMTCategoryDelta) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].DeltaCommittedKiB != cs[j].DeltaCommittedKiB {
			return cs[i].DeltaCommittedKiB > cs[j].DeltaCommittedKiB
		}
		if cs[i].DeltaReservedKiB != cs[j].DeltaReservedKiB {
			return cs[i].DeltaReservedKiB > cs[j].DeltaReservedKiB
		}
		return cs[i].Name < cs[j].Name
	})
}

// DiffNMTCallSites compares the call sites of two detail captures, returning
// only sites whose size changed, sorted by committed growth (largest first).
func DiffNMTCallSites(before, after []NMTCallSite) []NMTCallSiteDelta {
	type entry struct {
		delta NMTCallSiteDelta
		resB  int64
		resA  int64
	}
	byKey := map[string]*entry{}
	var order []string
	get := func(s NMTCallSite) *entry {
		k := s.Kind + "\x00" + strings.Join(s.Frames, "\n")
		if e, ok := byKey[k]; ok {
			return e
		}
		e := &entry{delta: NMTCallSiteDelta{Kind: s.Kind, Category: s.Category, Frames: s.Frames}}
		byKey[k] = e
		order = append(order, k)
		return e
	}
	for _, s := range before {
		e := get(s)
		e.delta.BeforeCommittedKiB += s.CommittedKiB
		e.resB += s.ReservedKiB
	}
	for _, s := range after {
		e := get(s)
		e.delta.AfterCommittedKiB += s.CommittedKiB
		e.resA += s.ReservedKiB
	}
	var out []NMTCallSiteDelta
	for _, k := range order {
		e := byKey[k]
		e.delta.DeltaCommittedKiB = e.delta.AfterCommittedKiB - e.delta.BeforeCommittedKiB
		e.delta.DeltaReservedKiB = e.resA - e.resB
		if e.delta.DeltaCommittedKiB != 0 || e.delta.DeltaReservedKiB != 0 {
			out = append(out, e.delta)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].DeltaCommittedKiB != out[j].DeltaCommittedKiB {
			return out[i].DeltaCommittedKiB > out[j].DeltaCommittedKiB
		}
		return out[i].DeltaReservedKiB > out[j].DeltaReservedKiB
	})
	return out
}

var (
	nmtDiffTotalRe    = regexp.MustCompile(`Total: reserved=(\d+)KB(?: ([+-]\d+)KB)?, committed=(\d+)KB(?: ([+-]\d+)KB)?`)
	nmtDiffCategoryRe = regexp.MustCompile(`(?m)^-\s+(.+?)\s+\(reserved=(\d+)KB(?: ([+-]\d+)KB)?, committed=(\d+)KB(?: ([+-]\d+)KB)?\)`)
)

// ParseNMTSummaryDiff parses `jcmd VM.native_memory summary.diff` output (the
// live state compared against a prior `VM.native_memory baseline`). Before
// values are derived as current minus the reported delta.
func ParseNMTSummaryDiff(output string) (NMTDiff, error) {
	lower := strings.ToLower(output)
	if output == "" || strings.Contains(lower, "not enabled") {
		return NMTDiff{}, ErrNMTDisabled
	}
	if strings.Contains(lower, "no baseline") || strings.Contains(lower, "baseline for comparison") {
		return NMTDiff{}, ErrNMTNoBaseline
	}
	var d NMTDiff
	if m := nmtDiffTotalRe.FindStringSubmatch(output); m != nil {
		d.AfterReservedKiB, d.DeltaReservedKiB = atoi64(m[1]), signedKiB(m[2])
		d.AfterCommittedKiB, d.DeltaCommittedKiB = atoi64(m[3]), signedKiB(m[4])
		d.BeforeReservedKiB = d.AfterReservedKiB - d.DeltaReservedKiB
		d.BeforeCommittedKiB = d.AfterCommittedKiB - d.DeltaCommittedKiB
	}
	for _, m := range nmtDiffCategoryRe.FindAllStringSubmatch(output, -1) {
		c := NMTCategoryDelta{
			Name:              strings.TrimSpace(m[1]),
			AfterReservedKiB:  atoi64(m[2]),
			DeltaReservedKiB:  signedKiB(m[3]),
			AfterCommittedKiB: atoi64(m[4]),
			DeltaCommittedKiB: signedKiB(m[5]),
		}
		c.BeforeReservedKiB = c.AfterReservedKiB - c.DeltaReservedKiB
		c.BeforeCommittedKiB = c.AfterCommittedKiB - c.DeltaCommittedKiB
		d.Categories = append(d.Categories, c)
	}
	sortNMTCategoryDeltas(d.Categories)
	return d, nil
}

func signedKiB(s string) int64 {
	return atoi64(strings.TrimPrefix(s, "+"))
}
