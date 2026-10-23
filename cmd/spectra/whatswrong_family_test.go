package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
)

func familyTestProcs(udids []string, children int, cpuEach float64) []process.Info {
	var procs []process.Info
	for i, udid := range udids {
		root := 1000 * (i + 1)
		procs = append(procs, process.Info{
			PID: root, PPID: 1, Command: "launchd_sim",
			FullCommandLine: "launchd_sim /Users/x/Library/Developer/CoreSimulator/Devices/" + udid + "/data/var/run/launchd_bootstrap.plist",
			Elapsed:         3 * time.Hour,
		})
		for c := 1; c <= children; c++ {
			procs = append(procs, process.Info{PID: root + c, PPID: root, Command: fmt.Sprintf("svc%d", c), CPUPct: cpuEach, Elapsed: 3 * time.Hour})
		}
	}
	return procs
}

func TestRankSimulatorFamiliesNamedByUDID(t *testing.T) {
	udids := []string{
		"AAAAAAAA-1111-2222-3333-444444444444",
		"BBBBBBBB-1111-2222-3333-444444444444",
		"CCCCCCCC-1111-2222-3333-444444444444",
	}
	procs := familyTestProcs(udids, 100, 2.5)
	procs = append(procs, process.Info{PID: 9, PPID: 1, Command: "git", CPUPct: 91})
	causes := rankSlowCauses(slowSignals{Procs: procs, Cores: 16})

	// Three family findings, all ranked before the single-process git one.
	if len(causes) != 4 {
		t.Fatalf("want 3 family causes + git cause, got %+v", causes)
	}
	for i, udid := range udids {
		c := causes[i]
		if c.Severity != "high" || !strings.Contains(c.Title, udid) || !strings.Contains(c.Title, "16%") || !strings.Contains(c.Title, "of machine CPU") {
			t.Errorf("cause %d = %+v, want high finding naming %s", i, c, udid)
		}
		if !strings.Contains(c.Detail, "250% CPU (2.5 cores busy) across 101 processes") || !strings.Contains(c.Detail, "3h0m") {
			t.Errorf("detail missing CPU/members/age: %q", c.Detail)
		}
	}
	if !strings.Contains(causes[3].Detail, "git (pid 9)") {
		t.Errorf("git single-process cause should rank last: %+v", causes[3])
	}
}

func TestFamilyCausesIgnoreSingletonsAndLowCPU(t *testing.T) {
	procs := []process.Info{
		{PID: 1, PPID: 0, Command: "git", CPUPct: 91},
		{PID: 2, PPID: 0, Command: "make", CPUPct: 10},
		{PID: 3, PPID: 2, Command: "clang", CPUPct: 10},
	}
	if got := appendFamilyCauses(nil, process.GroupFamilies(procs, time.Time{}, 0), 16); len(got) != 0 {
		t.Errorf("unexpected family causes: %+v", got)
	}
}

func TestFamilyCausesUnknownCoresFallBackToProcessShare(t *testing.T) {
	procs := familyTestProcs([]string{"AAAAAAAA-1111-2222-3333-444444444444"}, 10, 30)
	causes := rankSlowCauses(slowSignals{Procs: procs})
	if len(causes) == 0 || !strings.Contains(causes[0].Title, "of total process CPU") {
		t.Fatalf("causes = %+v", causes)
	}
}

func TestNoisyFamiliesNeedHigherBarAndCapMedium(t *testing.T) {
	chrome := func(n int, each float64) []process.Info {
		procs := []process.Info{{PID: 1, PPID: 0, Command: "Google Chrome", ExecutablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", CPUPct: each}}
		for i := 2; i <= n; i++ {
			procs = append(procs, process.Info{PID: i, PPID: 1, Command: "Google Chrome Helper", ExecutablePath: "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper", CPUPct: each})
		}
		return procs
	}
	// 300% raw on 16 cores: below both bars.
	if got := appendFamilyCauses(nil, process.GroupFamilies(chrome(10, 30), time.Time{}, 0), 16); len(got) != 0 {
		t.Errorf("300%% browser family should not alert: %+v", got)
	}
	// 600% raw: above the raw bar, severity capped at medium.
	got := appendFamilyCauses(nil, process.GroupFamilies(chrome(10, 60), time.Time{}, 0), 16)
	if len(got) != 1 || got[0].Severity != "medium" {
		t.Errorf("600%% browser family = %+v, want one medium cause", got)
	}
	// 50% of a 4-core machine (200% raw) trips the machine-share bar.
	if got := appendFamilyCauses(nil, process.GroupFamilies(chrome(10, 20), time.Time{}, 0), 4); len(got) != 1 {
		t.Errorf("machine-share bar not applied: %+v", got)
	}
}

func TestGroupByConflict(t *testing.T) {
	cases := []struct {
		args    []string
		groupBy string
		wantMsg bool
	}{
		{[]string{"--group-by", "family"}, "family", false},
		{[]string{"--group-by", "family", "--top", "3", "--json"}, "family", false},
		{[]string{"--group-by", "family", "--sort", "cpu"}, "family", true},
		{[]string{"--group-by", "family", "--ppid", "1"}, "family", true},
		{[]string{"--group-by", "family", "--user", "me"}, "family", true},
		{[]string{"--group-by", "family", "--tree"}, "family", true},
		{[]string{"--group-by", "nope"}, "nope", true},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.String("group-by", "", "")
		fs.String("sort", "rss", "")
		fs.Int("top", 0, "")
		fs.Int("ppid", -1, "")
		fs.String("user", "", "")
		fs.Bool("tree", false, "")
		fs.Bool("json", false, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatal(err)
		}
		if msg := groupByConflict(fs, c.groupBy); (msg != "") != c.wantMsg {
			t.Errorf("%v: msg = %q, wantMsg %v", c.args, msg, c.wantMsg)
		}
	}
}

func TestWhatswrongJSONIncludesFamilies(t *testing.T) {
	procs := familyTestProcs([]string{"AAAAAAAA-1111-2222-3333-444444444444"}, 5, 30)
	deps := slowDeps{
		load:  func() sysload.Load { return sysload.Load{} },
		power: func() sysinfo.PowerState { return sysinfo.PowerState{} },
		procs: func() []process.Info { return procs },
		now:   time.Now,
	}
	var out, errb bytes.Buffer
	if code := runWhatswrongWithIO([]string{"--json", "--no-daemon"}, &out, &errb, deps); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	var got struct {
		Families []process.Family `json:"families"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Families) != 1 || got.Families[0].UDID != "AAAAAAAA-1111-2222-3333-444444444444" || got.Families[0].Members != 6 {
		t.Errorf("families = %+v", got.Families)
	}
}

func TestEmitProcessFamilies(t *testing.T) {
	procs := familyTestProcs([]string{"AAAAAAAA-1111-2222-3333-444444444444"}, 3, 40)
	var text bytes.Buffer
	if code := emitProcessFamilies(&text, procs, time.Now(), 0, false); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"FAMILY", "Simulator AAAAAAAA-1111-2222-3333-444444444444", "120.0%", "3h0m"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("table missing %q:\n%s", want, text.String())
		}
	}
	var js bytes.Buffer
	if code := emitProcessFamilies(&js, procs, time.Now(), 1, true); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var fams []process.Family
	if err := json.Unmarshal(js.Bytes(), &fams); err != nil || len(fams) != 1 || fams[0].Kind != process.FamilySimulator {
		t.Errorf("json = %s err=%v", js.String(), err)
	}
}
