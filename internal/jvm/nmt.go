package jvm

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NMTCategory is one Native Memory Tracking category (Java Heap, Class, Thread,
// Code, GC, Internal, …) with its reserved and committed sizes.
type NMTCategory struct {
	Name         string `json:"name"`
	ReservedKiB  int64  `json:"reserved_kib"`
	CommittedKiB int64  `json:"committed_kib"`
}

// NMTBreakdown is the parsed result of `jcmd VM.native_memory summary`.
type NMTBreakdown struct {
	Enabled           bool          `json:"enabled"`
	TotalReservedKiB  int64         `json:"total_reserved_kib"`
	TotalCommittedKiB int64         `json:"total_committed_kib"`
	Categories        []NMTCategory `json:"categories,omitempty"`
}

var (
	nmtTotalRe    = regexp.MustCompile(`Total: reserved=(\d+)KB, committed=(\d+)KB`)
	nmtCategoryRe = regexp.MustCompile(`(?m)^-\s+(.+?)\s+\(reserved=(\d+)KB, committed=(\d+)KB\)`)
)

// ParseNMTSummary parses `VM.native_memory summary` output into a structured
// breakdown, categories sorted by committed size (largest first). When NMT is
// disabled or the output is empty, Enabled is false and there are no categories.
func ParseNMTSummary(output string) NMTBreakdown {
	if output == "" || strings.Contains(strings.ToLower(output), "not enabled") {
		return NMTBreakdown{Enabled: false}
	}
	b := NMTBreakdown{Enabled: true}
	if m := nmtTotalRe.FindStringSubmatch(output); m != nil {
		b.TotalReservedKiB = atoi64(m[1])
		b.TotalCommittedKiB = atoi64(m[2])
	}
	for _, m := range nmtCategoryRe.FindAllStringSubmatch(output, -1) {
		b.Categories = append(b.Categories, NMTCategory{
			Name:         strings.TrimSpace(m[1]),
			ReservedKiB:  atoi64(m[2]),
			CommittedKiB: atoi64(m[3]),
		})
	}
	sort.SliceStable(b.Categories, func(i, j int) bool {
		return b.Categories[i].CommittedKiB > b.Categories[j].CommittedKiB
	})
	return b
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
