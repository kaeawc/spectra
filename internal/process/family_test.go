package process

import (
	"fmt"
	"math"
	"testing"
	"time"
)

var familyNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

const (
	udidA = "AAAAAAAA-1111-2222-3333-444444444444"
	udidB = "BBBBBBBB-1111-2222-3333-444444444444"
	udidC = "CCCCCCCC-1111-2222-3333-444444444444"
)

func simProcs(basePID int, udid string, children int, cpuEach float64, age time.Duration) []Info {
	root := basePID
	procs := []Info{{
		PID: root, PPID: 1, Command: "launchd_sim",
		FullCommandLine: "launchd_sim /Users/x/Library/Developer/CoreSimulator/Devices/" + udid + "/data/var/run/launchd_bootstrap.plist",
		StartedAt:       familyNow.Add(-age), CPUPct: 1,
	}}
	for i := 1; i <= children; i++ {
		procs = append(procs, Info{
			PID: root + i, PPID: root, Command: fmt.Sprintf("svc%d", i),
			// system daemons in the runtime volume carry no UDID: resolved by ancestry
			ExecutablePath: "/Library/Developer/CoreSimulator/Volumes/iOS/usr/libexec/svc",
			StartedAt:      familyNow.Add(-age), CPUPct: cpuEach,
		})
	}
	return procs
}

func TestGroupFamiliesThreeSimulators(t *testing.T) {
	var procs []Info
	procs = append(procs, simProcs(1000, udidA, 100, 2.5, 3*time.Hour)...)
	procs = append(procs, simProcs(2000, udidB, 100, 2.5, 2*time.Hour)...)
	procs = append(procs, simProcs(3000, udidC, 100, 2.5, time.Minute)...)
	procs = append(procs, Info{PID: 9, PPID: 1, Command: "git", CPUPct: 91, StartedAt: familyNow.Add(-5 * time.Second)})

	fams := GroupFamilies(procs, familyNow, 2)
	sims := map[string]Family{}
	for _, f := range fams {
		if f.Kind == FamilySimulator {
			sims[f.UDID] = f
		}
	}
	if len(sims) != 3 {
		t.Fatalf("want 3 simulator families, got %d: %+v", len(sims), fams)
	}
	for udid, age := range map[string]time.Duration{udidA: 3 * time.Hour, udidB: 2 * time.Hour, udidC: time.Minute} {
		f := sims[udid]
		if f.Members != 101 {
			t.Errorf("%s members = %d, want 101", udid, f.Members)
		}
		if math.Abs(f.CPUPct-251) > 0.001 {
			t.Errorf("%s cpu = %v, want 251", udid, f.CPUPct)
		}
		if f.Age != age {
			t.Errorf("%s age = %v, want %v", udid, f.Age, age)
		}
		if len(f.Heaviest) != 2 {
			t.Errorf("%s heaviest = %d, want 2", udid, len(f.Heaviest))
		}
		wantShare := 251 / (3*251 + 91.0) * 100
		if math.Abs(f.SharePct-wantShare) > 0.01 {
			t.Errorf("%s share = %v, want %v", udid, f.SharePct, wantShare)
		}
	}
	if fams[len(fams)-1].Kind != FamilyOther && fams[0].Kind != FamilySimulator {
		t.Errorf("simulators should rank first: %+v", fams[0])
	}
	var git *Family
	for i := range fams {
		if fams[i].Key == "other:git" {
			git = &fams[i]
		}
	}
	if git == nil || git.Kind != FamilyOther || git.Members != 1 || git.CPUPct != 91 {
		t.Fatalf("git alone should stay an ungrouped singleton: %+v", git)
	}
}

func TestGroupFamiliesClassification(t *testing.T) {
	tests := []struct {
		name     string
		procs    []Info
		wantKeys map[string]int // key -> members
	}{
		{
			name: "xcodebuild children inherit xcode",
			procs: []Info{
				{PID: 10, PPID: 1, Command: "xcodebuild", CPUPct: 10},
				{PID: 11, PPID: 10, Command: "sh", CPUPct: 1},
				{PID: 12, PPID: 11, Command: "swift-frontend", CPUPct: 80},
			},
			wantKeys: map[string]int{FamilyXcode: 3},
		},
		{
			name: "build tools without xcode ancestor",
			procs: []Info{
				{PID: 10, PPID: 1, Command: "make"},
				{PID: 11, PPID: 10, Command: "clang"},
				{PID: 12, PPID: 1, Command: "java", FullCommandLine: "java -cp x org.gradle.launcher.daemon.bootstrap.GradleDaemon"},
			},
			wantKeys: map[string]int{FamilyBuild: 3},
		},
		{
			name: "browsers split by app",
			procs: []Info{
				{PID: 10, PPID: 1, Command: "Google Chrome", ExecutablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"},
				{PID: 11, PPID: 10, Command: "Google Chrome Helper", ExecutablePath: "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper"},
				{PID: 12, PPID: 1, Command: "firefox", ExecutablePath: "/usr/lib/firefox/firefox"},
				{PID: 13, PPID: 1, Command: "grep", FullCommandLine: "grep -r chrome ."},
			},
			wantKeys: map[string]int{"browser:Google Chrome": 2, "browser:Firefox": 1, "other:grep": 1},
		},
		{
			name: "emulator and qemu",
			procs: []Info{
				{PID: 10, PPID: 1, Command: "qemu-system-aarch64"},
				{PID: 11, PPID: 1, Command: "emulator"},
			},
			wantKeys: map[string]int{FamilyEmulator: 2},
		},
		{
			name: "simulator child with UDID in its own path and missing parent",
			procs: []Info{
				{PID: 20, PPID: 99999, Command: "MyApp", ExecutablePath: "/Users/x/Library/Developer/CoreSimulator/Devices/" + udidA + "/data/Containers/Bundle/MyApp"},
				{PID: 21, PPID: 99999, Command: "orphan"},
			},
			wantKeys: map[string]int{"simulator:" + udidA: 1, "other:orphan": 1},
		},
		{
			name: "ancestry cycle terminates",
			procs: []Info{
				{PID: 30, PPID: 31, Command: "a"},
				{PID: 31, PPID: 30, Command: "b"},
			},
			wantKeys: map[string]int{"other:a": 1, "other:b": 1},
		},
		{
			name: "lowercase UDID is normalised",
			procs: []Info{
				{PID: 40, PPID: 1, Command: "launchd_sim", FullCommandLine: "launchd_sim /x/CoreSimulator/Devices/aaaaaaaa-1111-2222-3333-444444444444/data"},
			},
			wantKeys: map[string]int{"simulator:" + udidA: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[string]int{}
			for _, f := range GroupFamilies(tt.procs, familyNow, 0) {
				got[f.Key] = f.Members
			}
			if len(got) != len(tt.wantKeys) {
				t.Fatalf("families = %v, want %v", got, tt.wantKeys)
			}
			for k, n := range tt.wantKeys {
				if got[k] != n {
					t.Errorf("family %q members = %d, want %d (all: %v)", k, got[k], n, got)
				}
			}
		})
	}
}

func TestGroupFamiliesEmptyAndAge(t *testing.T) {
	if got := GroupFamilies(nil, familyNow, 0); len(got) != 0 {
		t.Fatalf("empty input = %v", got)
	}
	procs := []Info{
		{PID: 1, Command: "make", Elapsed: 10 * time.Minute, CPUPct: 5},
		{PID: 2, PPID: 1, Command: "clang", StartTime: familyNow.Add(-time.Hour), CPUPct: 50},
	}
	f := GroupFamilies(procs, familyNow, 0)[0]
	if f.Age != time.Hour || f.StartedAt != familyNow.Add(-time.Hour) {
		t.Errorf("age/start = %v/%v", f.Age, f.StartedAt)
	}
	if f.Heaviest[0].PID != 2 || f.SharePct != 100 {
		t.Errorf("heaviest/share = %+v", f)
	}
}
