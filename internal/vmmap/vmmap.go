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
		name, sizes, ok := parseRegionRow(line)
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

	sort.SliceStable(s.Regions, func(i, j int) bool {
		if s.Regions[i].DirtyBytes != s.Regions[j].DirtyBytes {
			return s.Regions[i].DirtyBytes > s.Regions[j].DirtyBytes
		}
		return s.Regions[i].Type < s.Regions[j].Type
	})
	if topN > 0 && len(s.Regions) > topN {
		s.Regions = s.Regions[:topN]
	}
	return s
}

// parseRegionRow splits a region-table row into its type name and the first
// four size columns (virtual, resident, dirty, swapped). The type name may
// contain spaces, so the split point is the first run of four consecutive size
// tokens.
func parseRegionRow(line string) (name string, sizes [4]int64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return "", sizes, false
	}
	for i := 0; i+4 <= len(fields); i++ {
		if i == 0 {
			continue // a row always has a name before its numbers
		}
		if isSize(fields[i]) && isSize(fields[i+1]) && isSize(fields[i+2]) && isSize(fields[i+3]) {
			name = strings.Join(fields[:i], " ")
			for j := 0; j < 4; j++ {
				sizes[j] = parseSize(fields[i+j])
			}
			return name, sizes, true
		}
	}
	return "", sizes, false
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
