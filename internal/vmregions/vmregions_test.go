package vmregions

import "testing"

const sampleVmmap = `==== Non-writable regions for process 47118
REGION TYPE                    START - END         [ VSIZE  RSDNT  DIRTY   SWAP] PRT/MAX SHRMOD PURGE    REGION DETAIL
__TEXT                      102300000-102388000    [  544K   528K     0K     0K] r-x/r-x SM=COW          /bin/zsh
shared memory               1023b8000-1023c0000    [   32K    32K    32K     0K] r--/r-- SM=SHM
MALLOC_TINY                 300000000-300100000    [ 1024K   256K   200K     0K] rw-/rwx SM=PRV
JIT region                  400000000-400100000    [ 1024K   512K   512K     0K] rwx/rwx SM=PRV
__DATA                      500000000-500010000    [   64K    64K    40K     0K] rw-/rw- SM=COW          /usr/lib/foo.dylib
`

func TestParseComposition(t *testing.T) {
	c := Parse(sampleVmmap, 0)
	// Dirty totals: 0 + 32 + 200 + 512 + 40 = 784K
	if c.TotalDirtyBytes != 784*1024 {
		t.Errorf("total dirty = %d, want %d", c.TotalDirtyBytes, 784*1024)
	}
	// Shared-resident: only the SM=SHM region (32K).
	if c.SharedResidentBytes != 32*1024 {
		t.Errorf("shared resident = %d, want 32K", c.SharedResidentBytes)
	}
	// File-backed dirty: __TEXT (0K) + __DATA (40K) = 40K; anonymous = 32+200+512 = 744K.
	if c.FileBackedDirtyBytes != 40*1024 {
		t.Errorf("file-backed dirty = %d, want 40K", c.FileBackedDirtyBytes)
	}
	if c.AnonymousDirtyBytes != 744*1024 {
		t.Errorf("anonymous dirty = %d, want 744K", c.AnonymousDirtyBytes)
	}
	// Executable resident: __TEXT (528K, r-x) + JIT (512K, rwx) = 1040K.
	if c.ExecutableResidentBytes != 1040*1024 {
		t.Errorf("executable resident = %d, want 1040K", c.ExecutableResidentBytes)
	}
}

func TestParseRWXFlagged(t *testing.T) {
	c := Parse(sampleVmmap, 0)
	// Only the JIT region has current protection rwx (MALLOC_TINY is rw-/rwx — max only).
	if len(c.RWXRegions) != 1 {
		t.Fatalf("rwx regions = %d, want 1 (%+v)", len(c.RWXRegions), c.RWXRegions)
	}
	if c.RWXRegions[0].Type != "JIT region" || c.RWXRegions[0].Prot != "rwx" {
		t.Errorf("rwx region = %+v", c.RWXRegions[0])
	}
}

func TestParseTopDirty(t *testing.T) {
	c := Parse(sampleVmmap, 2)
	if len(c.TopDirty) != 2 {
		t.Fatalf("top = %d", len(c.TopDirty))
	}
	// Ranked by dirty: JIT (512K) then MALLOC_TINY (200K).
	if c.TopDirty[0].Type != "JIT region" || c.TopDirty[1].Type != "MALLOC_TINY" {
		t.Errorf("top order = %q, %q", c.TopDirty[0].Type, c.TopDirty[1].Type)
	}
}

func TestIsRWX(t *testing.T) {
	if !isRWX("rwx") || isRWX("rw-") || isRWX("r-x") || isRWX("---") {
		t.Error("isRWX classification wrong")
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{"0K": 0, "544K": 544 * 1024, "1.5M": int64(1.5 * float64(1<<20)), "2G": 2 << 30, "": 0}
	for in, want := range cases {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}
