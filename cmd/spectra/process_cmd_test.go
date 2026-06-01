package main

import (
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestProcessFiltersAndSort(t *testing.T) {
	procs := []process.Info{
		{PID: 10, PPID: 1, User: "root", RSSKiB: 1024, VSizeKiB: 4096, VSZBytes: 4096 * 1024, CPUPercent: 1, MemPercent: 0.1, Elapsed: time.Hour, Command: "short"},
		{PID: 20, PPID: 1, User: "root", RSSKiB: 4096, VSizeKiB: 8192, VSZBytes: 8192 * 1024, CPUPercent: 4, MemPercent: 0.4, Elapsed: 3 * time.Hour, Command: "long"},
		{PID: 30, PPID: 20, User: "alice", RSSKiB: 8192, VSizeKiB: 16384, VSZBytes: 16384 * 1024, CPUPercent: 2, MemPercent: 0.8, Elapsed: 2 * time.Hour, Command: "child"},
	}
	filtered := filterProcesses(procs, processFilters{
		minRSSBytes: 2 * 1024 * 1024,
		minElapsed:  2 * time.Hour,
		ppid:        1,
		user:        "root",
	})
	if len(filtered) != 1 || filtered[0].PID != 20 {
		t.Fatalf("filtered = %+v, want PID 20", filtered)
	}
	if !sortProcesses(procs, "elapsed") {
		t.Fatal("sort elapsed returned false")
	}
	if procs[0].PID != 20 || procs[1].PID != 30 || procs[2].PID != 10 {
		t.Fatalf("elapsed sort PIDs = %d,%d,%d", procs[0].PID, procs[1].PID, procs[2].PID)
	}
	if !sortProcesses(procs, "mem") {
		t.Fatal("sort mem returned false")
	}
	if procs[0].PID != 30 {
		t.Fatalf("mem sort first PID = %d, want 30", procs[0].PID)
	}
	if sortProcesses(procs, "bad") {
		t.Fatal("bad sort returned true")
	}
}

func TestParseProcessSize(t *testing.T) {
	cases := map[string]uint64{
		"":     0,
		"512":  512,
		"2K":   2 * 1024,
		"1.5M": 1536 * 1024,
		"3G":   3 * 1024 * 1024 * 1024,
	}
	for in, want := range cases {
		got, err := parseProcessSize(in)
		if err != nil {
			t.Fatalf("parseProcessSize(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("parseProcessSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseProcessSize("-1"); err == nil {
		t.Fatal("expected negative size error")
	}
}
