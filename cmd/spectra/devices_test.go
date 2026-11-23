package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/devices"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/proc"
	"github.com/kaeawc/spectra/internal/process"
)

func fakeDeviceHost(r proc.Runner, procs []process.Info) devices.Options {
	return devices.Options{
		OS:     hostos.Linux,
		Runner: r,
		Procs:  func(context.Context) []process.Info { return procs },
		Cwds:   func(context.Context, []int) map[int]string { return map[int]string{} },
		LookPath: func(name string) (string, error) {
			if name == "adb" {
				return "/usr/bin/adb", nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(string) string { return "" },
		Stat:   func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		Now:    func() time.Time { return time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC) },
	}
}

func TestDevicesJSON(t *testing.T) {
	r := proc.NewFake().OnExact("/usr/bin/adb", []string{"devices", "-l"}, proc.Response{Result: proc.Result{
		Stdout: []byte("List of devices attached\nemulator-5554 device product:sdk model:Pixel_7 device:emu transport_id:1\n"),
	}})
	server := process.Info{PID: 5, PPID: 1, Command: "adb", FullCommandLine: "adb -L tcp:5037 fork-server server"}
	var out, errBuf bytes.Buffer
	if code := runDevicesWithIO([]string{"--json"}, &out, &errBuf, fakeDeviceHost(r, []process.Info{server})); code != 0 {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	var rep devices.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("not JSON: %q", out.String())
	}
	if len(rep.Android.Devices) != 1 || !rep.Android.Devices[0].Emulator || rep.IOSSimulators.Status != devices.StatusUnsupported {
		t.Errorf("report = %+v", rep)
	}
}

func TestDevicesTextWithoutServer(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runDevicesWithIO(nil, &out, &errBuf, fakeDeviceHost(proc.NewFake(), nil)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"android     server_not_running", "simulators  unsupported", "devices  0 booted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDevicesRejectsArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runDevicesWithIO([]string{"extra"}, &out, &errBuf, fakeDeviceHost(proc.NewFake(), nil)); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}
