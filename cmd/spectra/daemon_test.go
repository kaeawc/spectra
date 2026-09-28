package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"golang.org/x/sys/unix"
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
		probe: func(daemon.Paths) (bool, int, error) { return false, 0, nil },
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

func testRunningDaemon(t *testing.T, daemonVersion string) (daemon.Paths, func()) {
	t.Helper()
	// A short /tmp path stays within the 104-byte Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	paths := daemon.Paths{Dir: dir, Socket: filepath.Join(dir, "daemon.sock"), Lock: filepath.Join(dir, "daemon.lock"), PID: filepath.Join(dir, "daemon.pid")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.New(daemon.Options{Paths: paths, Version: daemonVersion}).Run(ctx) }()
	for i := 0; i < 100; i++ {
		if c, ok := daemonclient.Discover(paths); ok {
			c.Close()
			return paths, func() { cancel(); <-done; os.RemoveAll(dir) }
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	os.RemoveAll(dir)
	t.Fatal("daemon did not start")
	return daemon.Paths{}, nil
}

func TestDaemonStartAlreadyRunningAndSpawned(t *testing.T) {
	paths, cleanup := testRunningDaemon(t, "dev")
	defer cleanup()
	deps, _ := fakeDaemonDeps("darwin")
	deps.discover = daemonclient.Discover
	deps.ensureRunning = func(context.Context, daemon.Paths, daemonclient.SpawnOptions) (*daemonclient.Client, error) {
		t.Fatal("spawned an already running daemon")
		return nil, nil
	}
	var out bytes.Buffer
	if err := daemonStart(paths, &out, deps); err != nil || !strings.Contains(out.String(), "already running") {
		t.Fatalf("already running: %q, %v", out.String(), err)
	}
	out.Reset()
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	spawned := false
	deps.ensureRunning = func(ctx context.Context, got daemon.Paths, opts daemonclient.SpawnOptions) (*daemonclient.Client, error) {
		spawned = true
		if got.Socket != paths.Socket || opts.Exe == "" || strings.Join(opts.Args, " ") != "daemon run" {
			t.Fatalf("spawn options: %+v", opts)
		}
		return daemonclient.Dial(ctx, paths.Socket)
	}
	if err := daemonStart(paths, &out, deps); err != nil || !spawned || !strings.Contains(out.String(), "started") {
		t.Fatalf("spawned: %q, %v", out.String(), err)
	}
}

func TestDaemonStatusFormatsAndMismatch(t *testing.T) {
	paths, cleanup := testRunningDaemon(t, "2")
	defer cleanup()
	deps, _ := fakeDaemonDeps("darwin")
	deps.discover = daemonclient.Discover
	oldVersion := version
	version = "2"
	defer func() { version = oldVersion }()
	var out bytes.Buffer
	if err := daemonStatus(nil, paths, &out, deps); err != nil || !strings.Contains(out.String(), "daemon running") {
		t.Fatalf("text: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := daemonStatus([]string{"--json"}, paths, &out, deps); err != nil || !strings.Contains(out.String(), `"version":"2"`) {
		t.Fatalf("json: %q, %v", out.String(), err)
	}
	version = "3"
	if err := daemonStatus(nil, paths, &out, deps); !errors.Is(err, daemonclient.ErrVersionMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	deps.paths = func() (daemon.Paths, error) { return paths, nil }
	var stderr bytes.Buffer
	if got := runDaemonWithIO([]string{"status"}, &out, &stderr, deps); got != 1 {
		t.Fatalf("version mismatch exit = %d, stderr %q", got, stderr.String())
	}
}

func TestDaemonStopRPCAndStaleFiles(t *testing.T) {
	paths, cleanup := testRunningDaemon(t, "dev")
	defer cleanup()
	deps := defaultDaemonDeps()
	var out bytes.Buffer
	if err := daemonStop(paths, &out, deps); err != nil || !strings.Contains(out.String(), "stopping") {
		t.Fatalf("RPC stop: %q, %v", out.String(), err)
	}
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(paths.PID); errors.Is(err, os.ErrNotExist) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := t.TempDir()
	stale := daemon.Paths{Dir: dir, Lock: filepath.Join(dir, "lock"), PID: filepath.Join(dir, "pid"), Socket: filepath.Join(dir, "sock")}
	if err := os.WriteFile(stale.PID, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", stale.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	deps.signal = func(int, os.Signal) error { t.Fatal("signaled stale PID"); return nil }
	out.Reset()
	if err := daemonStop(stale, &out, deps); err != nil || !strings.Contains(out.String(), "cleaned stale files") {
		t.Fatalf("stale stop: %q, %v", out.String(), err)
	}
	for _, path := range []string{stale.PID, stale.Socket} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale file remains %s: %v", path, err)
		}
	}
}

func TestDaemonStopLockedFallbackAndTimeout(t *testing.T) {
	// A short /tmp path stays within the 104-byte Unix socket sun_path limit.
	dir, err := os.MkdirTemp("/tmp", "spd-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	paths := daemon.Paths{Dir: dir, Lock: filepath.Join(dir, "lock"), PID: filepath.Join(dir, "pid"), Socket: filepath.Join(dir, "sock")}
	if err := os.WriteFile(paths.PID, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	lock, err := os.OpenFile(paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	deps := defaultDaemonDeps()
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	var signals []os.Signal
	deps.signal = func(_ int, sig os.Signal) error {
		signals = append(signals, sig)
		if sig == daemonTermSignal {
			return os.Remove(paths.PID)
		}
		return nil
	}
	deps.sleep = func(time.Duration) {}
	var out bytes.Buffer
	if err := daemonStop(paths, &out, deps); err != nil || !strings.Contains(out.String(), "stopped") || len(signals) != 2 {
		t.Fatalf("locked fallback: %q, %v, signals %v", out.String(), err, signals)
	}
	if err := os.WriteFile(paths.PID, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.signal = func(_ int, sig os.Signal) error { signals = append(signals, sig); return nil }
	if err := daemonStop(paths, &out, deps); err == nil || !strings.Contains(err.Error(), "within 5s") {
		t.Fatalf("timeout: %v", err)
	}
}

func TestDaemonStopSignalLivenessErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantError bool
	}{
		{"permission", syscall.EPERM, true},
		{"missing", syscall.ESRCH, false},
		{"unexpected", syscall.EIO, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _ := fakeDaemonDeps("darwin")
			deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
			deps.probe = func(daemon.Paths) (bool, int, error) { return true, 1234, nil }
			calls := 0
			deps.signal = func(pid int, sig os.Signal) error {
				calls++
				if pid != 1234 || sig != syscall.Signal(0) {
					t.Fatalf("signal(%d, %v)", pid, sig)
				}
				return tc.err
			}
			var out bytes.Buffer
			err := daemonStop(daemon.Paths{}, &out, deps)
			if (err != nil) != tc.wantError || (tc.wantError && !errors.Is(err, tc.err)) || calls != 1 {
				t.Fatalf("stop = %q, %v, calls %d", out.String(), err, calls)
			}
		})
	}
}

func TestDaemonStopUnknownPIDNeverSignalsZero(t *testing.T) {
	deps, _ := fakeDaemonDeps("darwin")
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	probes := 0
	deps.probe = func(daemon.Paths) (bool, int, error) {
		probes++
		if probes == 1 {
			return true, 0, nil
		}
		return false, 0, nil
	}
	deps.signal = func(pid int, _ os.Signal) error { t.Fatalf("signaled pid %d", pid); return nil }
	var out bytes.Buffer
	if err := daemonStop(daemon.Paths{}, &out, deps); err != nil || !strings.Contains(out.String(), "stopped") {
		t.Fatalf("stop = %q, %v", out.String(), err)
	}
	if probes != 2 {
		t.Fatalf("probes = %d", probes)
	}
}

func TestDaemonStatusUnknownPID(t *testing.T) {
	deps, _ := fakeDaemonDeps("darwin")
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	deps.probe = func(daemon.Paths) (bool, int, error) { return true, 0, nil }
	if err := daemonStatus(nil, daemon.Paths{}, io.Discard, deps); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("status = %v", err)
	}
}

func TestRunDaemonWithIOExitCodes(t *testing.T) {
	deps, _ := fakeDaemonDeps("darwin")
	deps.discover = func(daemon.Paths) (*daemonclient.Client, bool) { return nil, false }
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, 2}, {[]string{"start", "extra"}, 2}, {[]string{"unknown"}, 2}, {[]string{"status"}, 1},
	} {
		var out, stderr bytes.Buffer
		if got := runDaemonWithIO(tc.args, &out, &stderr, deps); got != tc.want {
			t.Fatalf("%v: exit %d, want %d; stderr %q", tc.args, got, tc.want, stderr.String())
		}
	}
}

func TestDaemonUnitEscaping(t *testing.T) {
	unit := daemonUnit(`/opt/Spectra % "Tools"\spectra`)
	if !strings.Contains(unit, `ExecStart="/opt/Spectra %% \"Tools\"\\spectra" daemon run`) {
		t.Fatal(unit)
	}
}
