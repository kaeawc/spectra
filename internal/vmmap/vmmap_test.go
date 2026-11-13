package vmmap

import (
	"os"
	"path/filepath"
	"testing"
)

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

// Real `vmmap -summary` captures from macOS 26.6 (Finder, and Slack, whose
// output starts with a malloc-zone warning).
func parseFixture(t *testing.T, name string) Summary {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return Parse(string(b), 0)
}

func findRegion(t *testing.T, rs []Region, typ string) Region {
	t.Helper()
	for _, r := range rs {
		if r.Type == typ {
			return r
		}
	}
	t.Fatalf("region %q not parsed; got %+v", typ, rs)
	return Region{}
}

func TestParseRealOutputKeepsColumnsForNumericTypeNames(t *testing.T) {
	const k = int64(1 << 10)
	m := float64(1 << 20) // a variable so fractional sizes truncate like parseSize
	cases := []struct {
		fixture string
		want    Region
	}{
		// "Memory Tag 22  64.0M 16K 16K 0K": the trailing 22 is part of the
		// name, not the virtual size.
		{"finder_summary.txt", Region{Type: "Memory Tag 22", VirtualBytes: int64(64.0 * m), ResidentBytes: 16 * k, DirtyBytes: 16 * k}},
		{"slack_summary.txt", Region{Type: "Memory Tag 253", VirtualBytes: 32 << 30, ResidentBytes: int64(71.5 * m), DirtyBytes: int64(36.4 * m)}},
		{"slack_summary.txt", Region{Type: "Memory Tag 255 (reserved)", VirtualBytes: 320 * k}},
		// Bare byte counts without a unit suffix.
		{"finder_summary.txt", Region{Type: "__CTF", VirtualBytes: 824, ResidentBytes: 824}},
		{"finder_summary.txt", Region{Type: "dyld private memory", VirtualBytes: 160 * k, ResidentBytes: 56, DirtyBytes: 56}},
		// Trailing annotation after the REGION COUNT column.
		{"finder_summary.txt", Region{Type: "MALLOC_SMALL", VirtualBytes: int64(74.3 * m), ResidentBytes: int64(28.7 * m), DirtyBytes: int64(27.5 * m)}},
	}
	for _, tc := range cases {
		t.Run(tc.fixture+"/"+tc.want.Type, func(t *testing.T) {
			got := findRegion(t, parseFixture(t, tc.fixture).Regions, tc.want.Type)
			if got != tc.want {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseRealOutputRegionsSumToTotal(t *testing.T) {
	for _, fixture := range []string{"finder_summary.txt", "slack_summary.txt"} {
		t.Run(fixture, func(t *testing.T) {
			s := parseFixture(t, fixture)
			if s.FootprintBytes == 0 || s.TotalDirtyBytes == 0 {
				t.Fatalf("footprint/total not parsed: %+v", s)
			}
			var dirty, swapped int64
			for _, r := range s.Regions {
				if r.Type == "" || r.Type == "TOTAL" {
					t.Errorf("bad region type %q", r.Type)
				}
				dirty += r.DirtyBytes
				swapped += r.SwappedBytes
			}
			// Rows are rounded to 0.1M, so allow 1% drift from TOTAL.
			if diff := dirty - s.TotalDirtyBytes; diff > s.TotalDirtyBytes/100 || -diff > s.TotalDirtyBytes/100 {
				t.Errorf("sum of region dirty = %d, TOTAL = %d", dirty, s.TotalDirtyBytes)
			}
			if swapped != s.TotalSwappedBytes {
				t.Errorf("sum of region swapped = %d, TOTAL = %d", swapped, s.TotalSwappedBytes)
			}
		})
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
