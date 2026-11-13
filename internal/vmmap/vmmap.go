// Package vmmap summarizes a process's memory composition from `vmmap
// --summary` output. A footprint number has no shape on its own; this parses
// the REGION TYPE table into per-region virtual/resident/dirty/swapped sizes so
// "where is this process's memory" is answerable at a glance. It reads only.
package vmmap

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Region is one row of the vmmap region-type summary.
type Region struct {
	Type          string `json:"type"`
	VirtualBytes  int64  `json:"virtual_bytes"`
	ResidentBytes int64  `json:"resident_bytes"`
	DirtyBytes    int64  `json:"dirty_bytes"`
	SwappedBytes  int64  `json:"swapped_bytes"`
}

// Summary is the parsed memory composition of a process.
type Summary struct {
	FootprintBytes     int64    `json:"footprint_bytes"`
	FootprintPeakBytes int64    `json:"footprint_peak_bytes,omitempty"`
	Regions            []Region `json:"regions"`
	TotalVirtualBytes  int64    `json:"total_virtual_bytes"`
	TotalResidentBytes int64    `json:"total_resident_bytes"`
	TotalDirtyBytes    int64    `json:"total_dirty_bytes"`
	TotalSwappedBytes  int64    `json:"total_swapped_bytes"`
}

var (
	footprintLine     = regexp.MustCompile(`^Physical footprint:\s+(\S+)`)
	footprintPeakLine = regexp.MustCompile(`^Physical footprint \(peak\):\s+(\S+)`)
	sizeToken         = regexp.MustCompile(`^\d+(\.\d+)?[KMGTB]?$`)
)

// Parse turns `vmmap --summary` output into a Summary. topN keeps only the
// heaviest-by-dirty region rows (0 keeps all).
func Parse(out string, topN int) Summary {
	var s Summary
	inTable := false
	valueCols := 0

	for _, line := range strings.Split(out, "\n") {
		if m := footprintPeakLine.FindStringSubmatch(line); m != nil {
			s.FootprintPeakBytes = parseSize(m[1])
			continue
		}
		if m := footprintLine.FindStringSubmatch(line); m != nil {
			s.FootprintBytes = parseSize(m[1])
			continue
		}
		if strings.HasPrefix(line, "REGION TYPE") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if strings.HasPrefix(line, "===") {
			// The separator has one run of '=' per column: the type name, then
			// one per value column.
			valueCols = len(strings.Fields(line)) - 1
			continue
		}
		name, sizes, ok := parseRegionRow(line, valueCols)
		if !ok {
			continue
		}
		if name == "TOTAL" {
			s.TotalVirtualBytes, s.TotalResidentBytes = sizes[0], sizes[1]
			s.TotalDirtyBytes, s.TotalSwappedBytes = sizes[2], sizes[3]
			break // the region table ends at TOTAL; a second table follows
		}
		s.Regions = append(s.Regions, Region{
			Type: name, VirtualBytes: sizes[0], ResidentBytes: sizes[1],
			DirtyBytes: sizes[2], SwappedBytes: sizes[3],
		})
	}

	s.Regions = rankRegions(s.Regions, topN)
	return s
}

// rankRegions orders regions heaviest-by-dirty first and keeps the top N
// (0 keeps all).
func rankRegions(rs []Region, topN int) []Region {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].DirtyBytes != rs[j].DirtyBytes {
			return rs[i].DirtyBytes > rs[j].DirtyBytes
		}
		return rs[i].Type < rs[j].Type
	})
	if topN > 0 && len(rs) > topN {
		return rs[:topN]
	}
	return rs
}

// parseRegionRow splits a region-table row into its type name and the first
// four of its valueCols size columns (virtual, resident, dirty, swapped).
// Values are anchored from the right because type names can end in a number
// that looks like a bare byte count ("Memory Tag 22"); a left-to-right scan
// for size tokens would start the columns one field early. Trailing
// non-size text is an annotation ("see MALLOC ZONE table below") and is
// dropped first.
func parseRegionRow(line string, valueCols int) (name string, sizes [4]int64, ok bool) {
	if valueCols < len(sizes) {
		return "", sizes, false
	}
	fields := strings.Fields(line)
	end := len(fields)
	for end > 0 && !isSize(fields[end-1]) {
		end--
	}
	start := end - valueCols
	if start < 1 {
		return "", sizes, false // a row always has a name before its numbers
	}
	for _, f := range fields[start:end] {
		if !isSize(f) {
			return "", sizes, false
		}
	}
	for j := range sizes {
		sizes[j] = parseSize(fields[start+j])
	}
	return strings.Join(fields[:start], " "), sizes, true
}

func isSize(s string) bool { return sizeToken.MatchString(s) }

// parseSize converts a vmmap size token like "128.0M" or "16K" into bytes.
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mult := int64(1)
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	case 'B':
		mult = 1
	}
	if mult != 1 || s[len(s)-1] == 'B' {
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(v * float64(mult))
}
