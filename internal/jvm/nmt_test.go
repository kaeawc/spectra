package jvm

import (
	"strings"
	"testing"
)

const nmtSample = `Native Memory Tracking:

Total: reserved=6685554KB, committed=397490KB

-                 Java Heap (reserved=4194304KB, committed=262144KB)
                            (mmap: reserved=4194304KB, committed=262144KB)

-                     Class (reserved=1048576KB, committed=81920KB)
                            (classes #12345)

-                    Thread (reserved=52428KB, committed=52428KB)
                            (thread #51)

-                      Code (reserved=249936KB, committed=45000KB)

-                        GC (reserved=204800KB, committed=30000KB)

-                  Internal (reserved=1024KB, committed=1024KB)
`

func TestParseNMTSummary(t *testing.T) {
	b := ParseNMTSummary(nmtSample)
	if !b.Enabled {
		t.Fatal("expected Enabled")
	}
	if b.TotalReservedKiB != 6685554 || b.TotalCommittedKiB != 397490 {
		t.Fatalf("totals = %d/%d", b.TotalReservedKiB, b.TotalCommittedKiB)
	}
	if len(b.Categories) != 6 {
		t.Fatalf("categories = %d, want 6: %+v", len(b.Categories), b.Categories)
	}
	// Sorted by committed size, largest first.
	if b.Categories[0].Name != "Java Heap" || b.Categories[0].CommittedKiB != 262144 {
		t.Errorf("top category = %+v, want Java Heap 262144", b.Categories[0])
	}
	if b.Categories[1].Name != "Class" || b.Categories[1].CommittedKiB != 81920 {
		t.Errorf("second = %+v, want Class 81920", b.Categories[1])
	}
	last := b.Categories[len(b.Categories)-1]
	if last.Name != "Internal" || last.CommittedKiB != 1024 {
		t.Errorf("last = %+v, want Internal 1024", last)
	}
	// Reserved is captured too.
	if b.Categories[0].ReservedKiB != 4194304 {
		t.Errorf("Java Heap reserved = %d, want 4194304", b.Categories[0].ReservedKiB)
	}
}

func TestNativeMemoryObservationReportsCategories(t *testing.T) {
	obs := nativeMemoryObservation(nmtSample)
	if obs.ID != "native-memory-available" {
		t.Fatalf("id = %q", obs.ID)
	}
	if !strings.Contains(obs.Summary, "committed 388MiB") {
		t.Errorf("summary = %q, want total committed ~388MiB (397490KiB)", obs.Summary)
	}
	if !strings.Contains(obs.Evidence, "Java Heap 256MiB") {
		t.Errorf("evidence should list top category: %q", obs.Evidence)
	}
	if obs.Recommendation == "" {
		t.Error("expected remediation guidance")
	}
}

func TestNativeMemoryObservationFallback(t *testing.T) {
	// Output present but unparseable into categories -> generic observation.
	obs := nativeMemoryObservation("Native Memory Tracking:\n(some unrecognized shape)\n")
	if obs.Evidence != "" {
		t.Fatalf("expected generic fallback observation, got %+v", obs)
	}
}

func TestParseNMTSummaryDisabled(t *testing.T) {
	for _, in := range []string{"", "Native memory tracking is not enabled\n"} {
		b := ParseNMTSummary(in)
		if b.Enabled || len(b.Categories) != 0 {
			t.Fatalf("ParseNMTSummary(%q) = %+v, want disabled/empty", in, b)
		}
	}
}
