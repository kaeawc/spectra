package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashreport"
)

func TestClassifyMemoryKill(t *testing.T) {
	mem := &crashreport.Report{Process: "Hungry", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "resident memory crossed a high-watermark limit", Limit: "1500MB", Observed: "1620MB"}}
	if k, ok := classifyMemoryKill(mem); !ok || k.Process != "Hungry" || k.Limit != "1500MB" {
		t.Errorf("MEMORY kill = %+v ok=%v, want included with limit", k, ok)
	}
	seg := &crashreport.Report{Process: "App", Exception: "EXC_BAD_ACCESS"}
	if _, ok := classifyMemoryKill(seg); ok {
		t.Error("a segfault must not be classified as an OOM kill")
	}
	cpu := &crashreport.Report{Process: "Busy", Resource: &crashreport.ResourceKill{Flavor: "CPU"}}
	if _, ok := classifyMemoryKill(cpu); ok {
		t.Error("a CPU EXC_RESOURCE must not be an OOM kill")
	}
	jet := &crashreport.Report{Process: "Jetted", BugType: "298"}
	if k, ok := classifyMemoryKill(jet); !ok || !strings.Contains(k.Reason, "jetsam") {
		t.Errorf("jetsam bug_type = %+v ok=%v, want jetsam kill", k, ok)
	}
}

func TestCollectMemoryKillsSortsAndFilters(t *testing.T) {
	swept := []sweptReport{
		{File: "/r/a.ips", Report: &crashreport.Report{Process: "A", Time: "2026-08-01 10:00:00.00 -0500", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "mem"}}},
		{File: "/r/seg.ips", Report: &crashreport.Report{Process: "Seg", Time: "2026-08-02 10:00:00.00 -0500", Exception: "EXC_BAD_ACCESS"}},
		{File: "/r/b.ips", Report: &crashreport.Report{Process: "B", Time: "2026-08-05 10:00:00.00 -0500", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "mem"}}},
	}
	kills := collectMemoryKills(swept)
	if len(kills) != 2 {
		t.Fatalf("kills = %d, want 2 (segfault excluded): %+v", len(kills), kills)
	}
	if kills[0].Process != "B" {
		t.Errorf("newest-first expected B, got %s", kills[0].Process)
	}
}

func memResourceIPS(app, when string) string {
	return `{"app_name":"` + app + `","timestamp":"` + when + `","bug_type":"309","name":"` + app + `"}
{"procName":"` + app + `","pid":1,"exception":{"type":"EXC_RESOURCE","subtype":"MEMORY (Limit 1500 MB Observed 1620 MB)"},"termination":{"namespace":"RESOURCE","indicator":"MEMORY"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestRunCrashOOMRenders(t *testing.T) {
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"mem.ips", "seg.ips"}, nil },
		read: func(f string) ([]byte, error) {
			if f == "mem.ips" {
				return []byte(memResourceIPS("Hungry", "2026-08-05 10:00:00.00 -0500")), nil
			}
			return []byte(ipsReport("Fine", "2026-08-05 09:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashOOMWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "Hungry") || strings.Contains(s, "Fine") {
		t.Errorf("expected only the memory kill (Hungry, not Fine); got:\n%s", s)
	}
	if !strings.Contains(s, "limit") {
		t.Errorf("expected the limit note; got:\n%s", s)
	}
}

func TestRunCrashOOMNone(t *testing.T) {
	deps := crashListDeps{
		list: func() ([]string, error) { return []string{"seg.ips"}, nil },
		read: func(string) ([]byte, error) {
			return []byte(ipsReport("Fine", "2026-08-05 09:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	runCrashOOMWithDeps(false, 25, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "No memory/OOM kills") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunCrashOOMRejectsPositional(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashOOM([]string{"extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
