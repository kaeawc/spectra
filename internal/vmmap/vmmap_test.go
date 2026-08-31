package vmmap

import "testing"

const sampleSummary = `Process:         Foo [4012]
Physical footprint:         1665K
Physical footprint (peak):  1681K

                                VIRTUAL RESIDENT    DIRTY  SWAPPED VOLATILE   NONVOL    EMPTY   REGION
REGION TYPE                        SIZE     SIZE     SIZE     SIZE     SIZE     SIZE     SIZE    COUNT (non-coalesced)
===========                     ======= ========    =====  ======= ========   ======    =====  =======
MALLOC guard page                   96K       0K       0K       0K       0K       0K       0K        6
MALLOC_SMALL                      40.0M     304K     304K       0K       0K       0K       0K        5         see MALLOC ZONE table below
Stack                             8176K      32K      32K       0K       0K       0K       0K        1
__DATA                             232K     232K     148K       0K       0K       0K       0K       46
unused but dirty shlib __DATA      112K     112K     112K       0K       0K       0K       0K       32
===========                     ======= ========    =====  ======= ========   ======    =====  =======
TOTAL                            929.0M   263.9M    1681K       8K       0K      16K       0K      299
                                 VIRTUAL   RESIDENT      DIRTY    SWAPPED ALLOCATION
MALLOC ZONE                         SIZE       SIZE       SIZE       SIZE      COUNT
DefaultMallocZone_0x1              176.0M       752K       752K         0K       2747
`

func TestParseFootprintAndTotal(t *testing.T) {
	s := Parse(sampleSummary, 0)
	if s.FootprintBytes != 1665*1024 {
		t.Errorf("footprint = %d", s.FootprintBytes)
	}
	if s.FootprintPeakBytes != 1681*1024 {
		t.Errorf("peak = %d", s.FootprintPeakBytes)
	}
	if s.TotalDirtyBytes != 1681*1024 || s.TotalSwappedBytes != 8*1024 {
		t.Errorf("totals dirty=%d swapped=%d", s.TotalDirtyBytes, s.TotalSwappedBytes)
	}
}

func TestParseRegionsRankedByDirtyAndNamesWithSpaces(t *testing.T) {
	s := Parse(sampleSummary, 0)
	// The MALLOC ZONE table after TOTAL must not be parsed as regions.
	for _, r := range s.Regions {
		if r.Type == "DefaultMallocZone_0x1" || r.Type == "TOTAL" {
			t.Errorf("region table bled past TOTAL: %q", r.Type)
		}
	}
	// Ranked by dirty desc: MALLOC_SMALL (304K) first.
	if s.Regions[0].Type != "MALLOC_SMALL" || s.Regions[0].DirtyBytes != 304*1024 {
		t.Errorf("top region = %+v", s.Regions[0])
	}
	// A space-containing type name must be preserved and its sizes parsed.
	if !hasRegion(s.Regions, "unused but dirty shlib __DATA", 112*1024) {
		t.Errorf("space-named region not parsed: %+v", s.Regions)
	}
	// MALLOC_SMALL's virtual is 40.0M.
	if s.Regions[0].VirtualBytes != int64(40.0*float64(1<<20)) {
		t.Errorf("MALLOC_SMALL virtual = %d", s.Regions[0].VirtualBytes)
	}
}

func TestParseTopN(t *testing.T) {
	s := Parse(sampleSummary, 2)
	if len(s.Regions) != 2 {
		t.Errorf("topN=2 gave %d regions", len(s.Regions))
	}
}

func TestParseSizes(t *testing.T) {
	cases := map[string]int64{
		"0K":     0,
		"16K":    16 * 1024,
		"128.0M": int64(128.0 * float64(1<<20)),
		"2G":     2 * (1 << 30),
		"512B":   512,
		"":       0,
		"junk":   0,
	}
	for in, want := range cases {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func hasRegion(rs []Region, typ string, dirty int64) bool {
	for _, r := range rs {
		if r.Type == typ && r.DirtyBytes == dirty {
			return true
		}
	}
	return false
}
