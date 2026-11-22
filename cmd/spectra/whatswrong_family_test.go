package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/sysinfo"
	"github.com/kaeawc/spectra/internal/sysload"
	"github.com/kaeawc/spectra/internal/whatswrong"
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
	deps := whatswrong.Deps{
		Load:  func(context.Context) sysload.Load { return sysload.Load{} },
		Power: func(context.Context) sysinfo.PowerState { return sysinfo.PowerState{} },
		Procs: func(context.Context) []process.Info { return procs },
		Now:   time.Now,
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
