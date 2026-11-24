package crashquery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashreport"
	"github.com/kaeawc/spectra/internal/hostos"
)

func ipsReport(app, when, exc string) string {
	return `{"app_name":"` + app + `","timestamp":"` + when + `","bug_type":"309","incident_id":"INC-` + app + `","name":"` + app + `"}
{"procName":"` + app + `","pid":1,"exception":{"type":"` + exc + `","signal":"SIGSEGV"},"termination":{"indicator":"x"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestSweepSkipsUnparseable(t *testing.T) {
	read := func(f string) ([]byte, error) {
		switch f {
		case "good.ips":
			return []byte(ipsReport("Good", "2026-08-05 10:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		case "legacy.crash":
			return []byte("Process: Old [1]\n"), nil // legacy plain-text
		default:
			return nil, os.ErrNotExist
		}
	}
	swept, skipped, err := Sweep(context.Background(), []string{"good.ips", "legacy.crash", "missing.ips"}, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(swept) != 1 || swept[0].Report.Process != "Good" {
		t.Errorf("swept = %+v, want 1 (Good)", swept)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (legacy + missing)", skipped)
	}
}

func TestSweepStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reads := 0
	read := func(string) ([]byte, error) {
		reads++
		cancel()
		return []byte(ipsReport("A", "2026-08-05 10:00:00.00 -0500", "EXC_CRASH")), nil
	}
	_, _, err := Sweep(ctx, []string{"a.ips", "b.ips", "c.ips"}, read)
	if !errors.Is(err, context.Canceled) || reads != 1 {
		t.Fatalf("err = %v after %d reads, want canceled after 1", err, reads)
	}
}

func rep(proc, exc, when, frame, incident string) Swept {
	r := &crashreport.Report{
		Process: proc, Exception: exc, Time: when, IncidentID: incident,
		FaultingThread: 0,
		Threads:        []crashreport.Thread{{Triggered: true, Frames: []string{frame}}},
	}
	return Swept{File: "/r/" + incident + ".ips", Report: r}
}

func TestAggregateSignaturesCollapsesRepeats(t *testing.T) {
	swept := []Swept{
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-01 09:00:00.00 -0500", "MyApp`flush + 1", "a"),
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-05 15:00:00.00 -0500", "MyApp`flush + 1", "b"),
		rep("MyApp", "EXC_BAD_ACCESS", "2026-08-03 12:00:00.00 -0500", "MyApp`flush + 1", "c"),
		rep("Other", "EXC_CRASH", "2026-08-02 10:00:00.00 -0500", "Other`boom + 9", "d"),
	}
	sigs := AggregateSignatures(swept)
	if len(sigs) != 2 {
		t.Fatalf("signatures = %d, want 2: %+v", len(sigs), sigs)
	}
	top := sigs[0]
	if top.Count != 3 || top.Process != "MyApp" {
		t.Errorf("top signature = %+v, want MyApp x3", top)
	}
	if !strings.HasPrefix(top.First, "2026-08-01") || !strings.HasPrefix(top.Last, "2026-08-05") {
		t.Errorf("first/last = %q/%q, want 08-01/08-05", top.First, top.Last)
	}
	// last-seen sample should be the most recent (incident b)
	if top.Incident != "b" {
		t.Errorf("sample incident = %q, want b (most recent)", top.Incident)
	}
}

func TestAggregateSignaturesNoFrameBucket(t *testing.T) {
	r := &crashreport.Report{Process: "P", Exception: "EXC_CRASH", Time: "2026-08-01 09:00:00.00 -0500", Threads: nil}
	sigs := AggregateSignatures([]Swept{{File: "/r/x.ips", Report: r}})
	if len(sigs) != 1 || sigs[0].TopFrame != "(no frame)" {
		t.Errorf("expected a (no frame) bucket, got %+v", sigs)
	}
}

func TestClassifyMemoryKill(t *testing.T) {
	mem := &crashreport.Report{Process: "Hungry", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "resident memory crossed a high-watermark limit", Limit: "1500MB", Observed: "1620MB"}}
	if k, ok := ClassifyMemoryKill(mem); !ok || k.Process != "Hungry" || k.Limit != "1500MB" {
		t.Errorf("MEMORY kill = %+v ok=%v, want included with limit", k, ok)
	}
	seg := &crashreport.Report{Process: "App", Exception: "EXC_BAD_ACCESS"}
	if _, ok := ClassifyMemoryKill(seg); ok {
		t.Error("a segfault must not be classified as an OOM kill")
	}
	cpu := &crashreport.Report{Process: "Busy", Resource: &crashreport.ResourceKill{Flavor: "CPU"}}
	if _, ok := ClassifyMemoryKill(cpu); ok {
		t.Error("a CPU EXC_RESOURCE must not be an OOM kill")
	}
	jet := &crashreport.Report{Process: "Jetted", BugType: "298"}
	if k, ok := ClassifyMemoryKill(jet); !ok || !strings.Contains(k.Reason, "jetsam") {
		t.Errorf("jetsam bug_type = %+v ok=%v, want jetsam kill", k, ok)
	}
}

func TestCollectMemoryKillsSortsAndFilters(t *testing.T) {
	swept := []Swept{
		{File: "/r/a.ips", Report: &crashreport.Report{Process: "A", Time: "2026-08-01 10:00:00.00 -0500", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "mem"}}},
		{File: "/r/seg.ips", Report: &crashreport.Report{Process: "Seg", Time: "2026-08-02 10:00:00.00 -0500", Exception: "EXC_BAD_ACCESS"}},
		{File: "/r/b.ips", Report: &crashreport.Report{Process: "B", Time: "2026-08-05 10:00:00.00 -0500", Resource: &crashreport.ResourceKill{Flavor: "MEMORY", Explanation: "mem"}}},
	}
	kills := CollectMemoryKills(swept)
	if len(kills) != 2 {
		t.Fatalf("kills = %d, want 2 (segfault excluded): %+v", len(kills), kills)
	}
	if kills[0].Process != "B" {
		t.Errorf("newest-first expected B, got %s", kills[0].Process)
	}
}

func TestFindIPSFilesWalksRetiredAndDotfiles(t *testing.T) {
	dir := t.TempDir()
	retired := filepath.Join(dir, "Retired")
	if err := os.Mkdir(retired, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "App-2026.ips"))
	write(filepath.Join(dir, ".hidden-2026.ips")) // dotfile report
	write(filepath.Join(retired, "Old-2026.ips"))
	write(filepath.Join(dir, "power.diag")) // must be ignored

	got, err := FindIPSFiles(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("found %d .ips, want 3: %v", len(got), got)
	}
	for _, g := range got {
		if filepath.Ext(g) != ".ips" {
			t.Errorf("non-.ips in results: %s", g)
		}
	}
}

func TestFindIPSFilesMissingDir(t *testing.T) {
	got, err := FindIPSFiles(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil || got != nil {
		t.Errorf("missing dir = %v, %v; want nil, nil", got, err)
	}
}

func TestFindIPSFilesCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FindIPSFiles(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestDefaultDir(t *testing.T) {
	if got := DefaultDir(hostos.Darwin, "/Users/me"); got != "/Users/me/Library/Logs/DiagnosticReports" {
		t.Errorf("darwin = %q", got)
	}
	if got := DefaultDir(hostos.Linux, "/home/me"); got != "" {
		t.Errorf("linux = %q, want none", got)
	}
	if got := DefaultDir(hostos.Darwin, ""); got != "" {
		t.Errorf("no home = %q, want none", got)
	}
}

func TestLoadErrors(t *testing.T) {
	read := func(p string) ([]byte, error) {
		if p == "/r/old.crash" {
			return []byte("Process: Old [1]\n"), nil
		}
		return nil, os.ErrNotExist
	}
	_, err := Load("/r/old.crash", read)
	if !errors.Is(err, crashreport.ErrLegacyFormat) || err.Error() != "/r/old.crash is a legacy plain-text crash report; spectra decodes modern .ips JSON reports." {
		t.Errorf("legacy err = %v", err)
	}
	_, err = Load("/r/none.ips", read)
	if !errors.Is(err, os.ErrNotExist) || err.Error() != "read /r/none.ips: file does not exist" {
		t.Errorf("missing err = %v", err)
	}
}
