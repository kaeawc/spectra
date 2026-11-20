package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/storagestate"
)

var loadNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func withProcs(deps loadDeps, procs []process.Info) loadDeps {
	deps.Procs = func(context.Context) []process.Info { return procs }
	deps.Now = func() time.Time { return loadNow }
	return deps
}

func TestRunLoadDevicesJSON(t *testing.T) {
	deps := withProcs(fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n"), []process.Info{
		{PID: 10, PPID: 1, Command: "go", CPUPct: 90, StartTime: loadNow.Add(-time.Hour)},
		{PID: 20, PPID: 10, Command: "emulator", FullCommandLine: "emulator -avd Phone", StartTime: loadNow.Add(-2 * time.Minute)},
		{PID: 21, PPID: 20, Command: "qemu-system-x86_64", StartTime: loadNow.Add(-2 * time.Minute), CPUPct: 40},
		{PID: 30, PPID: 1, Command: "qemu-system-aarch64", FullCommandLine: "qemu-system-aarch64 -avd Tablet", StartTime: loadNow.Add(-20 * time.Minute)},
	})
	for _, tc := range []struct {
		args   []string
		recent int
	}{{nil, 1}, {[]string{"--boot-window", "30m"}, 2}} {
		var out, errb bytes.Buffer
		if code := runLoadWithIO(append([]string{"--json", "--root", "/r"}, tc.args...), &out, &errb, deps); code != 0 {
			t.Fatalf("exit = %d; %s", code, errb.String())
		}
		var m struct {
			Devices hostload.Devices `json:"devices"`
		}
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		d := m.Devices
		if d.Emulators != 2 || d.Simulators != 0 || d.Booted != 2 || d.BootedRecently != tc.recent {
			t.Fatalf("args %v: devices = %+v", tc.args, d)
		}
		// pid 10 runs in /r/wt1, so the emulator it started belongs to /r.
		if len(d.ByRoot) != 1 || d.ByRoot[0].Root != "/r" || d.ByRoot[0].Booted != 1 {
			t.Fatalf("by root = %+v", d.ByRoot)
		}
	}
	var out, errb bytes.Buffer
	if code := runLoadWithIO(nil, &out, &errb, deps); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"devices  2 booted (0 simulators, 2 emulators), 1 in the last 5m0s", "device   emulator  Phone  pid 21", "Tablet"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("text lacks %q:\n%s", want, out.String())
		}
	}
	if code := runLoadWithIO([]string{"--boot-window", "0s"}, &out, &errb, deps); code != 2 {
		t.Fatalf("zero window exit = %d", code)
	}
}

func TestRunLoadCIRunnerCause(t *testing.T) {
	deps := withProcs(fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n"), []process.Info{
		{PID: 10, PPID: 1, Command: "go", CPUPct: 20},
		{PID: 40, PPID: 1, Command: "Runner.Worker", CPUPct: 1},
		{PID: 41, PPID: 40, Command: "swift-frontend", CPUPct: 250},
	})
	var out, errb bytes.Buffer
	if code := runLoadWithIO([]string{"--json", "--root", "/r"}, &out, &errb, deps); code != 0 {
		t.Fatalf("exit = %d; %s", code, errb.String())
	}
	var m struct {
		Cause    hostload.Cause       `json:"cause"`
		Families []hostload.FamilyRow `json:"families"`
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Cause.Kind != "ci-runner" || m.Cause.Runner != "GitHub Actions" || m.Families[0].Kind != "ci-runner" {
		t.Fatalf("cause = %+v families = %+v", m.Cause, m.Families)
	}
}

func indexingDeps() loadDeps {
	return withProcs(fakeLoadDeps("1.00 1 1 1/1 1", "MemAvailable: 16777216 kB\n"), []process.Info{
		{PID: 10, PPID: 1, Command: "go", CPUPct: 5},
		{PID: 50, PPID: 1, Command: "mds_stores", CPUPct: 120},
	})
}

// rootSpot mirrors hostload.RootSpotlight as JSON; SpotlightStatus only
// marshals.
type rootSpot struct {
	Root        string `json:"root"`
	Unsupported string `json:"unsupported"`
	Excluded    *bool  `json:"excluded"`
	Marker      string `json:"marker"`
	PrivacyList string `json:"privacy_list"`
	Volume      *struct {
		MountPoint string `json:"mount_point"`
		Status     string `json:"status"`
	} `json:"volume"`
	Size *hostload.RootSize `json:"size"`
	Hint string             `json:"hint"`
}

func spotlightOf(t *testing.T, deps loadDeps, args ...string) []rootSpot {
	t.Helper()
	var out, errb bytes.Buffer
	if code := runLoadWithIO(append([]string{"--json"}, args...), &out, &errb, deps); code != 0 {
		t.Fatalf("exit = %d; %s", code, errb.String())
	}
	var m struct {
		Cause     hostload.Cause `json:"cause"`
		Spotlight []rootSpot     `json:"spotlight"`
	}
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Cause.Kind != hostload.CauseIndexing {
		t.Fatalf("cause = %+v", m.Cause)
	}
	return m.Spotlight
}

func TestRunLoadIndexingSpotlightMacOS(t *testing.T) {
	deps := indexingDeps()
	deps.Spotlight = hostload.SpotlightProbe{
		OS:       hostos.Darwin,
		Exists:   func(p string) bool { return p == "/r/.metadata_never_index" },
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("permission denied") },
		Volume: func(context.Context, string) (storagestate.SpotlightVolume, error) {
			return storagestate.SpotlightVolume{MountPoint: "/System/Volumes/Data", Status: storagestate.SpotlightEnabled}, nil
		},
		Size: func(context.Context, string) (hostload.RootSize, error) {
			return hostload.RootSize{Files: 42, Bytes: 1 << 20}, nil
		},
	}
	got := spotlightOf(t, deps, "--root", "/r", "--root", "/s")
	if len(got) != 2 {
		t.Fatalf("spotlight = %+v", got)
	}
	r, s := got[0], got[1]
	if r.Excluded == nil || !*r.Excluded || r.Marker != "/r/.metadata_never_index" || r.Volume.Status != "enabled" || r.Size.Files != 42 {
		t.Fatalf("/r = %+v", r)
	}
	if s.Excluded != nil || s.PrivacyList != hostload.PrivacyUnreadable || !strings.Contains(s.Hint, "exclude it") {
		t.Fatalf("/s = %+v", s)
	}
	if got := spotlightOf(t, deps); len(got) != 0 {
		t.Fatalf("no roots should give no spotlight block: %+v", got)
	}
}

func TestRunLoadIndexingSpotlightUnsupportedOnLinux(t *testing.T) {
	got := spotlightOf(t, indexingDeps(), "--root", "/r")
	if len(got) != 1 || got[0].Unsupported == "" || got[0].Excluded != nil {
		t.Fatalf("spotlight = %+v", got)
	}
	var out, errb bytes.Buffer
	runLoadWithIO([]string{"--root", "/r"}, &out, &errb, indexingDeps())
	if !strings.Contains(out.String(), "spotlight /r  ") || !strings.Contains(out.String(), "unsupported on host OS (linux)") {
		t.Fatalf("text:\n%s", out.String())
	}
}
