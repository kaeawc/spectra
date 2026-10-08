package jvm

import (
	"regexp"
	"sort"
	"strings"
)

// NMT call-site kinds reported in the "Details:" section.
const (
	NMTSiteMalloc = "malloc"
	NMTSiteMmap   = "mmap"
)

// NMTCallSite is one allocation site from `VM.native_memory detail`: the
// native stack that allocated plus how much it holds. For malloc sites
// reserved and committed are both the malloc'd size.
type NMTCallSite struct {
	Kind         string   `json:"kind"`
	Category     string   `json:"category,omitempty"`
	Frames       []string `json:"frames"`
	ReservedKiB  int64    `json:"reserved_kib"`
	CommittedKiB int64    `json:"committed_kib"`
	Count        int64    `json:"count,omitempty"`
}

// NMTCommittedRegion is a committed sub-range of a reserved virtual region.
type NMTCommittedRegion struct {
	Start   string   `json:"start"`
	End     string   `json:"end"`
	SizeKiB int64    `json:"size_kib"`
	Frames  []string `json:"frames,omitempty"`
}

// NMTRegion is one reserved range from the detail "Virtual memory map:".
type NMTRegion struct {
	Start        string               `json:"start"`
	End          string               `json:"end"`
	Category     string               `json:"category,omitempty"`
	ReservedKiB  int64                `json:"reserved_kib"`
	CommittedKiB int64                `json:"committed_kib"`
	Frames       []string             `json:"frames,omitempty"`
	Committed    []NMTCommittedRegion `json:"committed,omitempty"`
}

// NMTDetail is the parsed result of `jcmd VM.native_memory detail`.
type NMTDetail struct {
	Summary NMTBreakdown  `json:"summary"`
	Sites   []NMTCallSite `json:"sites,omitempty"`
	Regions []NMTRegion   `json:"regions,omitempty"`
}

var (
	nmtFrameRe     = regexp.MustCompile(`^\[(0x[0-9a-fA-F]+)\]\s*(.*)$`)
	nmtReservedRe  = regexp.MustCompile(`^\[(0x[0-9a-fA-F]+) - (0x[0-9a-fA-F]+)\] reserved( and committed)? (\d+)KB(?: for (.+?))?(?: from)?$`)
	nmtCommittedRe = regexp.MustCompile(`^\[(0x[0-9a-fA-F]+) - (0x[0-9a-fA-F]+)\] committed (\d+)KB(?: from)?$`)
	nmtMallocRe    = regexp.MustCompile(`^\(malloc=(\d+)KB(?: type=([^#)]+?))?(?: #(\d+))?\)`)
	nmtMmapRe      = regexp.MustCompile(`^\(mmap: reserved=(\d+)KB, committed=(\d+)KB(?: ([^)]+))?\)`)
)

type nmtSection int

const (
	nmtSectionSummary nmtSection = iota
	nmtSectionVMMap
	nmtSectionDetails
)

// nmtDetailParser is a line-oriented state machine over the three sections of
// detail output: summary, "Virtual memory map:", and "Details:".
type nmtDetailParser struct {
	section   nmtSection
	frames    []string
	detail    NMTDetail
	region    *NMTRegion
	committed *NMTCommittedRegion
}

// ParseNMTDetail parses `VM.native_memory detail` output. The summary portion
// is parsed with ParseNMTSummary; malloc/mmap call sites are sorted by
// committed size (largest first) and virtual memory regions keep output order.
func ParseNMTDetail(output string) NMTDetail {
	p := nmtDetailParser{detail: NMTDetail{Summary: ParseNMTSummary(output)}}
	if !p.detail.Summary.Enabled {
		return p.detail
	}
	for _, line := range strings.Split(output, "\n") {
		p.line(strings.TrimSpace(line))
	}
	p.flushRegion()
	sort.SliceStable(p.detail.Sites, func(i, j int) bool {
		return p.detail.Sites[i].CommittedKiB > p.detail.Sites[j].CommittedKiB
	})
	return p.detail
}

func (p *nmtDetailParser) line(line string) {
	switch line {
	case "Virtual memory map:":
		p.section = nmtSectionVMMap
		return
	case "Details:":
		p.flushRegion()
		p.section = nmtSectionDetails
		return
	}
	switch p.section {
	case nmtSectionVMMap:
		p.vmMapLine(line)
	case nmtSectionDetails:
		p.detailsLine(line)
	}
}

func (p *nmtDetailParser) vmMapLine(line string) {
	if m := nmtReservedRe.FindStringSubmatch(line); m != nil {
		p.flushRegion()
		r := NMTRegion{Start: m[1], End: m[2], ReservedKiB: atoi64(m[4]), Category: m[5]}
		if m[3] != "" {
			r.CommittedKiB = r.ReservedKiB
		}
		p.region = &r
		return
	}
	if p.region == nil {
		return
	}
	if m := nmtCommittedRe.FindStringSubmatch(line); m != nil {
		p.flushCommitted()
		p.committed = &NMTCommittedRegion{Start: m[1], End: m[2], SizeKiB: atoi64(m[3])}
		return
	}
	frame, ok := nmtFrame(line)
	if !ok {
		return
	}
	if p.committed != nil {
		p.committed.Frames = append(p.committed.Frames, frame)
	} else {
		p.region.Frames = append(p.region.Frames, frame)
	}
}

func (p *nmtDetailParser) flushCommitted() {
	if p.committed == nil || p.region == nil {
		return
	}
	p.region.Committed = append(p.region.Committed, *p.committed)
	p.region.CommittedKiB += p.committed.SizeKiB
	p.committed = nil
}

func (p *nmtDetailParser) flushRegion() {
	p.flushCommitted()
	if p.region != nil {
		p.detail.Regions = append(p.detail.Regions, *p.region)
		p.region = nil
	}
}

func (p *nmtDetailParser) detailsLine(line string) {
	if line == "" {
		p.frames = nil
		return
	}
	if frame, ok := nmtFrame(line); ok {
		p.frames = append(p.frames, frame)
		return
	}
	if m := nmtMallocRe.FindStringSubmatch(line); m != nil {
		kib := atoi64(m[1])
		p.addSite(NMTCallSite{Kind: NMTSiteMalloc, Category: strings.TrimSpace(m[2]), ReservedKiB: kib, CommittedKiB: kib, Count: atoi64(m[3])})
		return
	}
	if m := nmtMmapRe.FindStringSubmatch(line); m != nil {
		p.addSite(NMTCallSite{Kind: NMTSiteMmap, Category: strings.TrimSpace(m[3]), ReservedKiB: atoi64(m[1]), CommittedKiB: atoi64(m[2])})
	}
}

func (p *nmtDetailParser) addSite(site NMTCallSite) {
	site.Frames = p.frames
	p.frames = nil
	p.detail.Sites = append(p.detail.Sites, site)
}

// nmtFrame returns a stack frame's symbol, dropping the address so the same
// call site matches across processes; unsymbolized frames keep the address.
func nmtFrame(line string) (string, bool) {
	m := nmtFrameRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	if m[2] != "" {
		return m[2], true
	}
	return m[1], true
}
