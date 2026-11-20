package hostload

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

const (
	simA = "AAAAAAAA-1111-2222-3333-444444444444"
	simB = "BBBBBBBB-1111-2222-3333-444444444444"
)

var devNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// simTree is one booted simulator: launchd_sim with its device path plus
// children that carry no UDID of their own.
func simTree(base int, udid string, children int, age time.Duration) []process.Info {
	start := devNow.Add(-age)
	out := []process.Info{{
		PID: base, PPID: 50, Command: "launchd_sim", StartTime: start, CPUPct: 1,
		FullCommandLine: "launchd_sim /Users/tester/Library/Developer/CoreSimulator/Devices/" + udid + "/data/var/run/launchd_bootstrap.plist",
	}}
	for i := 1; i <= children; i++ {
		out = append(out, process.Info{PID: base + i, PPID: base, Command: "svc", StartTime: start.Add(time.Second), CPUPct: 2,
			ExecutablePath: "/Library/Developer/CoreSimulator/Volumes/iOS/usr/libexec/svc"})
	}
	return out
}

// deviceTable: two simulators (one booted 2 minutes ago, one 2 hours ago),
// an emulator a lane under /wt started 1 minute ago (launcher + qemu), and a
// bare emulator launcher with no qemu below it started an hour ago.
func deviceTable() ([]process.Info, map[int]string) {
	procs := []process.Info{{PID: 50, PPID: 1, Command: "CoreSimulatorService"}}
	procs = append(procs, simTree(1000, simA, 20, 2*time.Minute)...)
	procs = append(procs, simTree(2000, simB, 20, 2*time.Hour)...)
	procs = append(procs,
		process.Info{PID: 300, PPID: 1, Command: "zsh", StartTime: devNow.Add(-time.Hour)},
		process.Info{PID: 301, PPID: 300, Command: "emulator", FullCommandLine: "emulator -avd Pixel_A -no-window", StartTime: devNow.Add(-time.Minute)},
		process.Info{PID: 302, PPID: 301, Command: "qemu-system-aarch64", FullCommandLine: "qemu-system-aarch64 -netdelay none", StartTime: devNow.Add(-50 * time.Second), CPUPct: 150},
		process.Info{PID: 400, PPID: 1, Command: "emulator", FullCommandLine: "emulator @Pixel_B", Elapsed: time.Hour},
	)
	cwds := map[int]string{300: "/wt/lane3", 301: "/", 302: "/"}
	return procs, cwds
}

func TestCollectDevices(t *testing.T) {
	procs, cwds := deviceTable()
	d := CollectDevices(procs, cwds, []Root{{Path: "/wt"}}, devNow, 0)
	if d.Simulators != 2 || d.Emulators != 2 || d.Booted != 4 {
		t.Fatalf("counts = %+v", d)
	}
	if d.BootedRecently != 2 || d.BootWindowSeconds != 300 {
		t.Fatalf("recent = %d window = %d", d.BootedRecently, d.BootWindowSeconds)
	}
	byID := map[string]Device{}
	for _, dev := range d.List {
		byID[dev.ID] = dev
	}
	a := byID[simA]
	if a.Kind != DeviceSimulator || a.UDID != simA || a.PID != 1000 || a.Members != 21 || !a.Recent || a.AgeSeconds != 120 {
		t.Fatalf("sim A = %+v", a)
	}
	if b := byID[simB]; b.Recent || b.AgeSeconds != 7200 {
		t.Fatalf("sim B = %+v", b)
	}
	e := byID["Pixel_A"]
	if e.Kind != DeviceEmulator || e.PID != 302 || e.Members != 2 || e.CPUPct != 150 || !e.Recent || e.Root != "/wt" {
		t.Fatalf("emulator A = %+v", e)
	}
	if o := byID["Pixel_B"]; o.PID != 400 || o.Recent || o.Root != "" || o.AgeSeconds != 3600 {
		t.Fatalf("emulator B = %+v", o)
	}
	if len(d.ByRoot) != 1 || d.ByRoot[0] != (RootDevices{Root: "/wt", Booted: 1, BootedRecently: 1}) {
		t.Fatalf("by root = %+v", d.ByRoot)
	}
}

func TestCollectDevicesWindowFlag(t *testing.T) {
	procs, cwds := deviceTable()
	d := CollectDevices(procs, cwds, nil, devNow, 3*time.Hour)
	if d.BootedRecently != 4 || len(d.ByRoot) != 0 || d.ByRoot == nil {
		t.Fatalf("devices = %+v", d)
	}
}

func TestAnalyzeCarriesDevices(t *testing.T) {
	procs, cwds := deviceTable()
	a := Analyze(procs, cwds, Options{Now: devNow, Roots: []Root{{Path: "/wt"}}, BootWindow: time.Minute})
	if a.Devices.Booted != 4 || a.Devices.BootedRecently != 1 || a.Devices.BootWindowSeconds != 60 {
		t.Fatalf("devices = %+v", a.Devices)
	}
}

func TestQemuCountedOnlyForAndroid(t *testing.T) {
	procs := []process.Info{
		{PID: 10, PPID: 1, Command: "limactl", FullCommandLine: "limactl hostagent colima"},
		{PID: 11, PPID: 10, Command: "qemu-system-aarch64", ExecutablePath: "/opt/homebrew/bin/qemu-system-aarch64",
			FullCommandLine: "qemu-system-aarch64 -m 4096 -name colima", CPUPct: 80},
		{PID: 20, PPID: 1, Command: "qemu-system-x86_64", ExecutablePath: "/sdk/emulator/qemu/darwin-x86_64/qemu-system-x86_64",
			FullCommandLine: "qemu-system-x86_64 -netdelay none"},
		{PID: 30, PPID: 1, Command: "qemu-system-x86_64", FullCommandLine: "qemu-system-x86_64 -avd Pixel_C"},
	}
	d := CollectDevices(procs, nil, nil, devNow, 0)
	if d.Emulators != 2 {
		t.Fatalf("emulators = %d, want 2 (not colima): %+v", d.Emulators, d.List)
	}
	for _, dev := range d.List {
		if dev.PID == 11 {
			t.Fatalf("colima qemu counted: %+v", dev)
		}
	}
}

func TestEmptyDevicesListEncodesAsArray(t *testing.T) {
	b, err := json.Marshal(CollectDevices(nil, nil, nil, devNow, 0))
	if err != nil || !strings.Contains(string(b), `"list":[]`) {
		t.Fatalf("json = %s, err %v", b, err)
	}
}

func TestAVDName(t *testing.T) {
	for in, want := range map[string]string{
		"emulator -avd Pixel_7 -no-snapshot": "Pixel_7",
		"emulator -avd=Tablet":               "Tablet",
		"emulator @Wear":                     "Wear",
		"qemu-system-x86_64 -m 2048":         "",
		"@notacommand":                       "",
	} {
		if got := AVDName(in); got != want {
			t.Errorf("AVDName(%q) = %q, want %q", in, got, want)
		}
	}
}
