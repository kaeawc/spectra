// Package vmregions aggregates a process's full `vmmap` region list by sharing,
// protection, and backing — answering how much of the footprint is private
// dirty vs shared, file-backed vs anonymous, and whether any region is RWX
// (writable and executable at once). It reads only.
package vmregions

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Region is one parsed vmmap region.
type Region struct {
	Type          string `json:"type"`
	AddrStart     string `json:"addr_start"`
	AddrEnd       string `json:"addr_end"`
	Prot          string `json:"prot"`
	MaxProt       string `json:"max_prot"`
	Share         string `json:"share"`
	Detail        string `json:"detail,omitempty"`
	ResidentBytes int64  `json:"resident_bytes"`
	DirtyBytes    int64  `json:"dirty_bytes"`
}

// Composition is the aggregated memory picture of a process.
type Composition struct {
	TotalResidentBytes      int64    `json:"total_resident_bytes"`
	TotalDirtyBytes         int64    `json:"total_dirty_bytes"`
	SharedResidentBytes     int64    `json:"shared_resident_bytes"`
	FileBackedDirtyBytes    int64    `json:"file_backed_dirty_bytes"`
	AnonymousDirtyBytes     int64    `json:"anonymous_dirty_bytes"`
	WritableResidentBytes   int64    `json:"writable_resident_bytes"`
	ExecutableResidentBytes int64    `json:"executable_resident_bytes"`
	RWXRegions              []Region `json:"rwx_regions,omitempty"`
	TopDirty                []Region `json:"top_dirty,omitempty"`
}

// regionLine matches "TYPE start-end [ VSIZE RSDNT DIRTY SWAP] PRT/MAX SM=xx DETAIL".
var regionLine = regexp.MustCompile(
	`^(.*?\S)\s+([0-9a-f]+)-([0-9a-f]+)\s+\[\s*(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*\]\s+(\S+)/(\S+)\s+SM=(\S+)\s*(.*)$`)

// Parse aggregates full `vmmap` output. topN keeps that many top-dirty regions.
func Parse(out string, topN int) Composition {
	var c Composition
	var all []Region
	for _, line := range strings.Split(out, "\n") {
		m := regionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		r := Region{
			Type: strings.TrimSpace(m[1]), AddrStart: m[2], AddrEnd: m[3],
			ResidentBytes: parseSize(m[5]), DirtyBytes: parseSize(m[6]),
			Prot: m[8], MaxProt: m[9], Share: m[10], Detail: sanitize(strings.TrimSpace(m[11])),
		}
		all = append(all, r)
		accumulate(&c, r)
	}

	for _, r := range all {
		if isRWX(r.Prot) {
			c.RWXRegions = append(c.RWXRegions, r)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].DirtyBytes != all[j].DirtyBytes {
			return all[i].DirtyBytes > all[j].DirtyBytes
		}
		return all[i].AddrStart < all[j].AddrStart
	})
	if topN > 0 && len(all) > topN {
		all = all[:topN]
	}
	c.TopDirty = all
	return c
}

func accumulate(c *Composition, r Region) {
	c.TotalResidentBytes += r.ResidentBytes
	c.TotalDirtyBytes += r.DirtyBytes
	if r.Share == "SHM" || r.Share == "ALI" {
		c.SharedResidentBytes += r.ResidentBytes
	}
	if r.Detail != "" && strings.HasPrefix(r.Detail, "/") {
		c.FileBackedDirtyBytes += r.DirtyBytes
	} else {
		c.AnonymousDirtyBytes += r.DirtyBytes
	}
	if strings.Contains(r.Prot, "w") {
		c.WritableResidentBytes += r.ResidentBytes
	}
	if strings.Contains(r.Prot, "x") {
		c.ExecutableResidentBytes += r.ResidentBytes
	}
}

// isRWX reports whether a current protection string is writable and executable
// at once (a W^X violation).
func isRWX(prot string) bool {
	return strings.Contains(prot, "w") && strings.Contains(prot, "x")
}

// parseSize converts a vmmap size token like "544K" or "1.2M" into bytes.
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

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
}
