package devices

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/hostload"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

const adbFixture = `* daemon not running; starting now at tcp:5037
* daemon started successfully
List of devices attached
emulator-5554          device product:sdk_gphone64_x86_64 model:sdk_gphone64_x86_64 device:emu64xa transport_id:1
192.168.1.20:5555      device product:panther model:Pixel_7 device:panther transport_id:3
adb-2A1B._adb-tls-connect._tcp offline transport_id:4
R58M12345              unauthorized usb:1-1 transport_id:2

`

func TestParseADBDevices(t *testing.T) {
	got := ParseADBDevices(adbFixture)
	want := []AndroidDevice{
		{Serial: "emulator-5554", State: "device", Emulator: true, Model: "sdk_gphone64_x86_64", Product: "sdk_gphone64_x86_64", Device: "emu64xa", TransportID: "1"},
		{Serial: "192.168.1.20:5555", State: "device", Remote: true, Model: "Pixel_7", Product: "panther", Device: "panther", TransportID: "3"},
		{Serial: "adb-2A1B._adb-tls-connect._tcp", State: "offline", Remote: true, TransportID: "4"},
		{Serial: "R58M12345", State: "unauthorized", TransportID: "2"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d devices: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if devs := ParseADBDevices("List of devices attached\n\n"); devs == nil || len(devs) != 0 {
		t.Errorf("no devices = %#v, want an empty non-nil slice", devs)
	}
}

const simctlFixture = `{"devices": {
  "com.apple.CoreSimulator.SimRuntime.iOS-17-2": [
    {"udid": "B2", "name": "iPhone 15", "state": "Booted"},
    {"udid": "B9", "name": "iPhone SE", "state": "Shutdown"}
  ],
  "com.apple.CoreSimulator.SimRuntime.watchOS-10-2": [
    {"udid": "A1", "name": "Apple Watch", "state": "Booted"}
  ]
}}`

func TestParseSimctlBooted(t *testing.T) {
	got, err := ParseSimctlBooted([]byte(simctlFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []Simulator{
		{UDID: "A1", Name: "Apple Watch", Runtime: "watchOS 10.2", State: "Booted"},
		{UDID: "B2", Name: "iPhone 15", Runtime: "iOS 17.2", State: "Booted"},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if _, err := ParseSimctlBooted([]byte("not json")); err == nil {
		t.Error("want an error for bad JSON")
	}
}

type fakeHost struct {
	paths map[string]string
	env   map[string]string
	files map[string]bool
	procs []process.Info
}

func (f fakeHost) options(osKind hostos.Kind, r proc.Runner) Options {
	return Options{
		OS:     osKind,
		Runner: r,
		Procs:  func(context.Context) []process.Info { return f.procs },
		Cwds:   func(context.Context, []int) map[int]string { return map[int]string{} },
		LookPath: func(name string) (string, error) {
			if p, ok := f.paths[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(k string) string { return f.env[k] },
		Stat: func(p string) (os.FileInfo, error) {
			if f.files[p] {
				return fakeFile{}, nil
			}
			return nil, os.ErrNotExist
		},
		Now: func() time.Time { return time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC) },
	}
}

type fakeFile struct{ os.FileInfo }

func (fakeFile) IsDir() bool { return false }

var adbServer = process.Info{PID: 900, PPID: 1, Command: "adb", FullCommandLine: "adb -L tcp:5037 fork-server server --reply-fd 4"}

func TestCollectAndroidWithRunningServer(t *testing.T) {
	r := proc.NewFake().OnExact("/sdk/platform-tools/adb", []string{"devices", "-l"}, proc.Response{Result: proc.Result{Stdout: []byte(adbFixture)}})
	h := fakeHost{
		env:   map[string]string{"ANDROID_HOME": "/sdk"},
		files: map[string]bool{"/sdk/platform-tools/adb": true},
		procs: []process.Info{adbServer},
	}
	rep := Collect(context.Background(), h.options(hostos.Linux, r))
	if rep.Android.Status != StatusOK || rep.Android.ADB != "/sdk/platform-tools/adb" || len(rep.Android.Devices) != 4 {
		t.Fatalf("android = %+v", rep.Android)
	}
	if rep.OS != "linux" || rep.IOSSimulators.Status != StatusUnsupported || rep.IOSSimulators.Booted == nil {
		t.Errorf("simulators off macOS = %+v", rep.IOSSimulators)
	}
}

func TestCollectAndroidDoesNotStartServer(t *testing.T) {
	r := proc.NewFake() // any command fails the test via the error below
	h := fakeHost{paths: map[string]string{"adb": "/usr/bin/adb"}}
	rep := Collect(context.Background(), h.options(hostos.Linux, r))
	if rep.Android.Status != StatusServerNotRunning || rep.Android.Devices == nil {
		t.Errorf("android = %+v, want server_not_running with an empty list", rep.Android)
	}
	if n := r.CallCount(); n != 0 {
		t.Errorf("ran %d command(s); adb must not be asked without a server: %+v", n, r.Calls())
	}
}

func TestCollectAndroidStartADBOrRemoteServer(t *testing.T) {
	for name, h := range map[string]fakeHost{
		"remote server env": {paths: map[string]string{"adb": "/usr/bin/adb"}, env: map[string]string{"ADB_SERVER_SOCKET": "tcp:10.0.0.2:5037"}},
		"start requested":   {paths: map[string]string{"adb": "/usr/bin/adb"}},
	} {
		r := proc.NewFake().OnExact("/usr/bin/adb", []string{"devices", "-l"}, proc.Response{Result: proc.Result{Stdout: []byte("List of devices attached\n")}})
		o := h.options(hostos.Linux, r)
		o.StartADB = name == "start requested"
		if rep := Collect(context.Background(), o); rep.Android.Status != StatusOK {
			t.Errorf("%s: android = %+v", name, rep.Android)
		}
	}
}

func TestCollectAndroidErrors(t *testing.T) {
	rep := Collect(context.Background(), fakeHost{}.options(hostos.Linux, proc.NewFake()))
	if rep.Android.Status != StatusNotFound {
		t.Errorf("no adb = %+v", rep.Android)
	}
	r := proc.NewFake().OnExact("/usr/bin/adb", []string{"devices", "-l"}, proc.Response{Result: proc.Result{ExitCode: 1, Stderr: []byte("protocol fault")}})
	h := fakeHost{paths: map[string]string{"adb": "/usr/bin/adb"}, procs: []process.Info{adbServer}}
	rep = Collect(context.Background(), h.options(hostos.Linux, r))
	if rep.Android.Status != StatusError || rep.Android.Message == "" || rep.Android.Devices == nil {
		t.Errorf("adb failure = %+v", rep.Android)
	}
}

func TestCollectSimulatorsOnDarwin(t *testing.T) {
	r := proc.NewFake().OnExact("/usr/bin/xcrun", []string{"simctl", "list", "devices", "booted", "--json"}, proc.Response{Result: proc.Result{Stdout: []byte(simctlFixture)}})
	h := fakeHost{paths: map[string]string{"xcrun": "/usr/bin/xcrun"}}
	rep := Collect(context.Background(), h.options(hostos.Darwin, r))
	if rep.IOSSimulators.Status != StatusOK || len(rep.IOSSimulators.Booted) != 2 {
		t.Errorf("simulators = %+v", rep.IOSSimulators)
	}

	r = proc.NewFake().On(proc.MatchPrefix("/usr/bin/xcrun", "simctl"), proc.Response{Result: proc.Result{ExitCode: 72, Stderr: []byte("unable to find utility \"simctl\"")}})
	rep = Collect(context.Background(), h.options(hostos.Darwin, r))
	if rep.IOSSimulators.Status != StatusError || rep.IOSSimulators.Booted == nil {
		t.Errorf("simctl failure = %+v", rep.IOSSimulators)
	}
}

func TestCollectCountsEmulatorProcessesUnderRoots(t *testing.T) {
	start := time.Date(2026, 11, 23, 9, 58, 0, 0, time.UTC)
	h := fakeHost{procs: []process.Info{
		{PID: 10, PPID: 1, Command: "emulator", FullCommandLine: "emulator -avd Pixel_7"},
		{PID: 11, PPID: 10, Command: "qemu-system-x86_64", FullCommandLine: "qemu-system-x86_64 -avd Pixel_7", StartTime: start},
	}}
	o := h.options(hostos.Linux, proc.NewFake())
	o.Cwds = func(context.Context, []int) map[int]string { return map[int]string{10: "/wt/lane-a"} }
	o.Roots = []hostload.Root{{Path: "/wt"}}
	rep := Collect(context.Background(), o)
	if rep.Processes.Emulators != 1 || rep.Processes.BootedRecently != 1 || len(rep.Processes.ByRoot) != 1 {
		t.Errorf("processes = %+v", rep.Processes)
	}
}
