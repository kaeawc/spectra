package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
)

func fakeDaemonDeps(goos string) (daemonDeps, *[]string) {
	calls := []string{}
	d := daemonDeps{
		paths:      func() (daemon.Paths, error) { return daemon.Paths{Log: "/tmp/spd-log/daemon.jsonl"}, nil },
		executable: func() (string, error) { return "/opt/Spectra & Tools/spectra", nil },
		home:       func() (string, error) { return "/home/test", nil },
		goos:       goos, uid: func() int { return 42 },
		mkdirAll:  func(string, os.FileMode) error { return nil },
		writeFile: func(string, []byte, os.FileMode) error { return nil },
		remove:    func(string) error { return nil },
		readFile:  func(string) ([]byte, error) { return nil, os.ErrNotExist },
		run: func(name string, args ...string) error {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil
		},
		output: func(name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return nil, errors.New("not loaded")
		},
		sleep: func(time.Duration) {},
	}
	return d, &calls
}

func TestDaemonServiceRendering(t *testing.T) {
	mac, _ := fakeDaemonDeps("darwin")
	_, plist, err := daemonServiceContent(mac)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"dev.spectra.daemon", "<key>SuccessfulExit</key><false/>", "<key>LowPriorityIO</key><true/>", "<key>ThrottleInterval</key><integer>30</integer>", "<string>daemon</string><string>run</string>", "Spectra &amp; Tools"} {
		if !strings.Contains(plist, fragment) {
			t.Errorf("plist missing %q", fragment)
		}
	}
	linux, _ := fakeDaemonDeps("linux")
	_, unit, err := daemonServiceContent(linux)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"ExecStart=\"/opt/Spectra & Tools/spectra\" daemon run", "Restart=on-failure", "CPUWeight=20", "IOSchedulingClass=idle"} {
		if !strings.Contains(unit, fragment) {
			t.Errorf("unit missing %q", fragment)
		}
	}
}

func TestDaemonInstallFakeServiceManagers(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			deps, calls := fakeDaemonDeps(goos)
			var out bytes.Buffer
			if err := daemonInstall(daemon.Paths{Log: "/tmp/spd-log/daemon.jsonl"}, &out, deps); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(*calls, "\n")
			if goos == "darwin" && !strings.Contains(joined, "launchctl bootstrap gui/42") {
				t.Fatal(joined)
			}
			if goos == "linux" && !strings.Contains(joined, "systemctl --user enable --now spectra-daemon.service") {
				t.Fatal(joined)
			}
			if err := daemonUninstall(&out, deps); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDaemonLogsTail(t *testing.T) {
	deps, _ := fakeDaemonDeps("darwin")
	deps.readFile = func(string) ([]byte, error) { return []byte("a\nb\nc\n"), nil }
	var out bytes.Buffer
	if err := daemonLogs([]string{"-n", "2"}, daemon.Paths{Log: filepath.Join("/tmp", "daemon.jsonl")}, &out, deps); err != nil {
		t.Fatal(err)
	}
	if out.String() != "b\nc\n" {
		t.Fatal(out.String())
	}
}
