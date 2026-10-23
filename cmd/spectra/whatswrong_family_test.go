package main

import (
	"bytes"
	"encoding/json"
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
	causes := rankSlowCauses(slowSignals{Procs: procs})

	// Three family findings, all ranked before the single-process git one.
	if len(causes) != 4 {
		t.Fatalf("want 3 family causes + git cause, got %+v", causes)
	}
	for i, udid := range udids {
		c := causes[i]
		if c.Severity != "high" || !strings.Contains(c.Title, udid) || !strings.Contains(c.Title, "of total CPU") {
			t.Errorf("cause %d = %+v, want high finding naming %s", i, c, udid)
		}
		if !strings.Contains(c.Detail, "250% CPU across 101 processes") || !strings.Contains(c.Detail, "3h0m") {
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
	if got := appendFamilyCauses(nil, process.GroupFamilies(procs, time.Time{}, 0)); len(got) != 0 {
		t.Errorf("unexpected family causes: %+v", got)
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
