package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/devices"
	"github.com/kaeawc/spectra/internal/hostload"
)

const devicesUsage = `usage: spectra devices [--json] [--root <dir>]... [--start-adb]

Lists the mobile devices work on this host may be using: Android devices and
emulators adb can reach (local, USB, or remote via adb connect), booted iOS
simulators (macOS, xcrun simctl), and emulator and simulator processes on
this host, attributed to the --root they run under. adb is asked only when an
adb server is already running, unless --start-adb is given, because asking
starts a server that keeps running afterwards.
`

func runDevices(args []string) int {
	return runDevicesWithIO(args, os.Stdout, os.Stderr, devices.Options{})
}

func runDevicesWithIO(args []string, stdout, stderr io.Writer, opts devices.Options) int {
	fs := flag.NewFlagSet("spectra devices", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, devicesUsage) }
	asJSON := fs.Bool("json", false, "Emit JSON")
	startADB := fs.Bool("start-adb", false, "Ask adb even when no adb server runs (starts one)")
	var roots stringList
	fs.Var(&roots, "root", "Directory whose emulators and simulators to count (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	opts.StartADB = *startADB
	opts.Roots = hostload.ResolveRoots(roots, filepath.Abs, filepath.EvalSymlinks)
	rep := devices.Collect(context.Background(), opts)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
		return 0
	}
	printDevices(stdout, rep)
	return 0
}

func printDevices(w io.Writer, r devices.Report) {
	a := r.Android
	switch a.Status {
	case devices.StatusOK:
		fmt.Fprintf(w, "android     %d via %s\n", len(a.Devices), a.ADB)
		for _, d := range a.Devices {
			kind := "usb"
			if d.Emulator {
				kind = "emulator"
			} else if d.Remote {
				kind = "remote"
			}
			fmt.Fprintf(w, "  %-28s %-12s %-8s %s\n", d.Serial, d.State, kind, d.Model)
		}
	default:
		fmt.Fprintf(w, "android     %s: %s\n", a.Status, a.Message)
	}
	s := r.IOSSimulators
	if s.Status == devices.StatusOK {
		fmt.Fprintf(w, "simulators  %d booted\n", len(s.Booted))
		for _, sim := range s.Booted {
			fmt.Fprintf(w, "  %-38s %-12s %s\n", sim.UDID, sim.Runtime, sim.Name)
		}
	} else {
		fmt.Fprintf(w, "simulators  %s: %s\n", s.Status, s.Message)
	}
	writeDevices(w, r.Processes)
}
