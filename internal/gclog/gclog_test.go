package gclog

import (
	"math"
	"strings"
	"testing"
)

const sampleLog = `[0.100s][info][gc] GC(0) Pause Young (Normal) (G1 Evacuation Pause) 25M->5M(256M) 4.123ms
[0.050s][info][gc,start] GC(0) Pause Young (Normal)
[0.100s][info][gc,phases] GC(0)   Evacuate Collection Set: 3.0ms
[0.200s][info][gc] GC(1) Pause Young (Concurrent Start) (G1 Humongous Allocation) 60M->30M(256M) 6.500ms
[1.000s][info][gc] GC(2) Pause Full (System.gc()) 100M->40M(256M) 45.678ms
[2.000s][info][gc] GC(3) Pause Young (Allocation Failure) 200M->190M(256M) 12.000ms to-space exhausted
random unrelated line
`

func approx(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func TestParseGCLog(t *testing.T) {
	s := Parse(strings.NewReader(sampleLog))
	if s.Pauses != 4 {
		t.Fatalf("pauses = %d, want 4", s.Pauses)
	}
	if s.YoungGCCount != 3 || s.FullGCCount != 1 {
		t.Errorf("young/full = %d/%d, want 3/1", s.YoungGCCount, s.FullGCCount)
	}
	if s.SystemGCCount != 1 {
		t.Errorf("system.gc count = %d, want 1", s.SystemGCCount)
	}
	if s.EvacuationFailures != 1 {
		t.Errorf("evacuation failures = %d, want 1", s.EvacuationFailures)
	}
	if !approx(s.TotalPauseMs, 68.301) {
		t.Errorf("total pause = %v, want ~68.301", s.TotalPauseMs)
	}
	if !approx(s.MaxPauseMs, 45.678) {
		t.Errorf("max pause = %v, want 45.678", s.MaxPauseMs)
	}
	if !approx(s.AvgPauseMs, 68.301/4) {
		t.Errorf("avg pause = %v", s.AvgPauseMs)
	}
	if s.LongestPause == nil || s.LongestPause.Kind != "Full" || s.LongestPause.ID != 2 {
		t.Errorf("longest pause = %+v, want the Full GC(2)", s.LongestPause)
	}
	if s.Causes["(System.gc())"] != 1 {
		t.Errorf("causes = %+v, want a System.gc() entry", s.Causes)
	}
}

func TestParsePauseDetail(t *testing.T) {
	s := Parse(strings.NewReader("[info][gc] GC(0) Pause Young (Normal) (G1 Evacuation Pause) 25M->5M(256M) 4.123ms\n"))
	if s.LongestPause == nil {
		t.Fatal("expected a pause")
	}
	p := *s.LongestPause
	if p.BeforeMB != 25 || p.AfterMB != 5 || p.HeapMB != 256 {
		t.Errorf("heap transition = %d->%d(%d), want 25->5(256)", p.BeforeMB, p.AfterMB, p.HeapMB)
	}
	if p.Cause != "(Normal) (G1 Evacuation Pause)" {
		t.Errorf("cause = %q", p.Cause)
	}
}

func TestParseHeapUnits(t *testing.T) {
	// Gigabyte and kilobyte units normalize to MiB.
	s := Parse(strings.NewReader("[info][gc] GC(0) Pause Full (System.gc()) 2G->512M(4G) 100.0ms\n"))
	p := s.LongestPause
	if p == nil || p.BeforeMB != 2048 || p.HeapMB != 4096 {
		t.Fatalf("unit conversion = %+v, want before 2048 / heap 4096", p)
	}
}

func TestParseEmpty(t *testing.T) {
	s := Parse(strings.NewReader("no gc lines here\n"))
	if s.Pauses != 0 || s.LongestPause != nil || s.AvgPauseMs != 0 {
		t.Fatalf("empty log summary = %+v", s)
	}
}
