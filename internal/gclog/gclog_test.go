package gclog

import (
	"math"
	"os"
	"path/filepath"
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

func TestParseFileTailWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gc.log")
	if err := os.WriteFile(path, []byte(sampleLog), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseFileTail(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if s.Pauses != 4 || s.FullGCCount != 1 {
		t.Fatalf("summary = %+v, want 4 pauses / 1 full", s)
	}
}

func TestParseFileTailDropsPartialFirstLine(t *testing.T) {
	last := "[3.000s][info][gc] GC(9) Pause Young (Normal) (G1 Evacuation Pause) 20M->4M(256M) 2.000ms\n"
	// The tail starts inside the Full GC line; its fragment must not be parsed.
	body := "[1.000s][info][gc] GC(2) Pause Full (System.gc()) 100M->40M(256M) 45.678ms\n" + last
	path := filepath.Join(t.TempDir(), "gc.log")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseFileTail(path, int64(len(last)+10))
	if err != nil {
		t.Fatal(err)
	}
	if s.Pauses != 1 || s.FullGCCount != 0 || s.LongestPause.ID != 9 {
		t.Fatalf("summary = %+v, want only GC(9)", s)
	}
}

func TestParseFileTailMissing(t *testing.T) {
	if _, err := ParseFileTail(filepath.Join(t.TempDir(), "nope.log"), 0); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestMerge(t *testing.T) {
	a := Parse(strings.NewReader(sampleLog))
	b := Parse(strings.NewReader("[info][gc] GC(0) Pause Full (Allocation Failure) 200M->180M(256M) 900.0ms\n"))
	m := Merge(a, b)
	if m.Pauses != 5 || m.FullGCCount != 2 || m.EvacuationFailures != 1 || m.SystemGCCount != 1 {
		t.Fatalf("merged counts = %+v", m)
	}
	if !approx(m.TotalPauseMs, 968.301) || !approx(m.MaxPauseMs, 900) || !approx(m.AvgPauseMs, 968.301/5) {
		t.Fatalf("merged times = total %v max %v avg %v", m.TotalPauseMs, m.MaxPauseMs, m.AvgPauseMs)
	}
	if m.LongestPause == nil || m.LongestPause.PauseMs != 900 {
		t.Fatalf("longest = %+v", m.LongestPause)
	}
	if m.Causes["(Allocation Failure)"] != 2 {
		t.Fatalf("causes = %+v", m.Causes)
	}
	if a.Pauses != 4 || a.Causes["(Allocation Failure)"] != 1 {
		t.Fatal("Merge mutated its input")
	}
}
