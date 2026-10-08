package jvm

import (
	"errors"
	"testing"
)

func TestDiffNMTSummaries(t *testing.T) {
	before := ParseNMTSummary(nmtSample)
	after := NMTBreakdown{
		Enabled:           true,
		TotalReservedKiB:  6685554 + 70000,
		TotalCommittedKiB: 397490 + 60000,
		Categories: []NMTCategory{
			{Name: "Java Heap", ReservedKiB: 4194304, CommittedKiB: 262144},
			{Name: "Thread", ReservedKiB: 102428, CommittedKiB: 102428},
			{Name: "Other", ReservedKiB: 20000, CommittedKiB: 20000},
			{Name: "Internal", ReservedKiB: 1024, CommittedKiB: 0},
		},
	}
	d := DiffNMTSummaries(before, after)
	if d.DeltaReservedKiB != 70000 || d.DeltaCommittedKiB != 60000 {
		t.Fatalf("totals = %+v", d)
	}
	if len(d.Categories) != 7 {
		t.Fatalf("categories = %d: %+v", len(d.Categories), d.Categories)
	}
	if c := d.Categories[0]; c.Name != "Thread" || c.DeltaCommittedKiB != 50000 || c.BeforeCommittedKiB != 52428 {
		t.Errorf("top = %+v", c)
	}
	if c := d.Categories[1]; c.Name != "Other" || c.BeforeCommittedKiB != 0 || c.DeltaCommittedKiB != 20000 {
		t.Errorf("new category = %+v", c)
	}
	if c := d.Categories[2]; c.Name != "Java Heap" || c.DeltaCommittedKiB != 0 {
		t.Errorf("unchanged = %+v", c)
	}
	if c := d.Categories[len(d.Categories)-1]; c.Name != "Class" || c.AfterCommittedKiB != 0 || c.DeltaCommittedKiB != -81920 {
		t.Errorf("vanished category should sort last: %+v", c)
	}
}

func TestDiffNMTCallSites(t *testing.T) {
	before := []NMTCallSite{
		{Kind: NMTSiteMalloc, Category: "Other", Frames: []string{"Unsafe_AllocateMemory0+0x84"}, ReservedKiB: 1024, CommittedKiB: 1024},
		{Kind: NMTSiteMalloc, Category: "Thread", Frames: []string{"Thread::allocate"}, ReservedKiB: 12, CommittedKiB: 12},
	}
	after := []NMTCallSite{
		{Kind: NMTSiteMalloc, Category: "Other", Frames: []string{"Unsafe_AllocateMemory0+0x84"}, ReservedKiB: 9216, CommittedKiB: 9216},
		{Kind: NMTSiteMalloc, Category: "Thread", Frames: []string{"Thread::allocate"}, ReservedKiB: 12, CommittedKiB: 12},
		{Kind: NMTSiteMalloc, Category: "Compiler", Frames: []string{"Arena::grow"}, ReservedKiB: 512, CommittedKiB: 512},
	}
	got := DiffNMTCallSites(before, after)
	if len(got) != 2 {
		t.Fatalf("deltas = %+v, want unchanged site omitted", got)
	}
	if got[0].Category != "Other" || got[0].DeltaCommittedKiB != 8192 || got[0].BeforeCommittedKiB != 1024 {
		t.Errorf("top = %+v", got[0])
	}
	if got[1].Category != "Compiler" || got[1].DeltaCommittedKiB != 512 {
		t.Errorf("new site = %+v", got[1])
	}
}

func TestParseNMTSummaryDiff(t *testing.T) {
	d, err := ParseNMTSummaryDiff(readNMTFixture(t, "nmt_summary_diff_jdk17.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if d.DeltaReservedKiB != 34816 || d.DeltaCommittedKiB != 4100 || d.BeforeCommittedKiB != 340142 {
		t.Fatalf("totals = %+v", d)
	}
	if len(d.Categories) != 5 {
		t.Fatalf("categories = %+v", d.Categories)
	}
	if c := d.Categories[0]; c.Name != "Thread" || c.DeltaCommittedKiB != 2100 || c.BeforeReservedKiB != 25796 {
		t.Errorf("top = %+v", c)
	}
	if c := d.Categories[len(d.Categories)-1]; c.Name != "Internal" || c.DeltaCommittedKiB != -14 || c.BeforeCommittedKiB != 543 {
		t.Errorf("shrunk = %+v", c)
	}
}

func TestParseNMTSummaryDiffErrors(t *testing.T) {
	if _, err := ParseNMTSummaryDiff("1:\nNative memory tracking is not enabled\n"); !errors.Is(err, ErrNMTDisabled) {
		t.Errorf("disabled err = %v", err)
	}
	if _, err := ParseNMTSummaryDiff("1:\nNo baseline for comparison\n"); !errors.Is(err, ErrNMTNoBaseline) {
		t.Errorf("baseline err = %v", err)
	}
}
