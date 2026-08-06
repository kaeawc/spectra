package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func memSnap(ramBytes uint64, procs ...process.Info) snapshot.Snapshot {
	var s snapshot.Snapshot
	s.Host.RAMBytes = ramBytes
	s.Processes = procs
	return s
}

func TestProcessMemoryHogFiresAboveThreshold(t *testing.T) {
	const ram = 16 * 1024 * 1024 * 1024 // 16 GiB
	// 6 GiB resident = 37% of RAM and above the 2 GiB floor.
	s := memSnap(ram, process.Info{PID: 900, Command: "hungry", RSSKiB: 6 * 1024 * 1024})
	findings := matchProcessMemoryHog(s)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].RuleID != "process-memory-hog" || !strings.Contains(findings[0].Message, "hungry") {
		t.Fatalf("finding = %+v", findings[0])
	}
}

func TestProcessMemoryHogRespectsFloorAndShare(t *testing.T) {
	const ram = 64 * 1024 * 1024 * 1024 // 64 GiB
	s := memSnap(ram,
		process.Info{PID: 1, Command: "small", RSSKiB: 500 * 1024},           // below 2 GiB floor
		process.Info{PID: 2, Command: "modest", RSSKiB: 3 * 1024 * 1024},     // above floor but ~4.7% of RAM
	)
	if f := matchProcessMemoryHog(s); len(f) != 0 {
		t.Fatalf("expected no findings, got %+v", f)
	}
}

func TestProcessMemoryHogSkipsUnknownRAM(t *testing.T) {
	s := memSnap(0, process.Info{PID: 3, Command: "x", RSSKiB: 100 * 1024 * 1024})
	if f := matchProcessMemoryHog(s); f != nil {
		t.Fatalf("expected nil when RAM unknown, got %+v", f)
	}
}
