package helper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeNetCaptureProcess struct {
	done chan error
}

func (p *fakeNetCaptureProcess) Wait() error {
	return <-p.done
}

func TestNetCaptureStartBuildsBoundedTCPDump(t *testing.T) {
	var gotName string
	var gotArgs []string
	var gotWriterPath string
	proc := &fakeNetCaptureProcess{done: make(chan error, 1)}
	baseDir := t.TempDir()
	if err := os.Chmod(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	m := newNetCaptureManager(func(_ context.Context, stdout, stderr io.Writer, name string, args ...string) (netCaptureProcess, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		file, ok := stdout.(*os.File)
		if !ok || file.Name() == "" || stderr == stdout {
			t.Fatalf("stdout = %T, stderr shares stdout = %v", stdout, stderr == stdout)
		}
		gotWriterPath = file.Name()
		if _, err := stdout.Write([]byte("pcap")); err != nil {
			t.Fatal(err)
		}
		return proc, nil
	}, baseDir)
	m.ownerUID = os.Geteuid()

	res, err := m.start(uid, netCaptureStartParams{
		Interface:  "en0",
		DurationMS: 5000,
		SnapLen:    4096,
		Proto:      "tcp",
		Host:       "api.example.com",
		Port:       443,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if gotName != "tcpdump" {
		t.Fatalf("command = %q, want tcpdump", gotName)
	}
	output := res["output_path"].(string)
	if gotWriterPath != output {
		t.Fatalf("stdout file = %q, want pre-created %q", gotWriterPath, output)
	}
	if matched, _ := regexp.MatchString(`^netcap-1-[0-9a-f]{32}\.pcap$`, filepath.Base(output)); !matched {
		t.Fatalf("output = %q, want randomized capture name", output)
	}
	if filepath.Dir(output) != filepath.Join(baseDir, fmt.Sprint(uid)) {
		t.Fatalf("output = %q, want per-UID directory", output)
	}
	wantArgs := []string{"-i", "en0", "-n", "-s", "4096", "-U", "-w", "-", "tcp", "and", "host", "api.example.com", "and", "port", "443"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	if res["handle"] != "netcap-1" || res["output_path"] != output {
		t.Fatalf("result = %+v", res)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "pcap" {
		t.Fatalf("capture bytes = %q, %v", data, err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("capture mode = %v, %v", info, err)
	}

	proc.done <- nil
	if _, err := m.stop(uid, netCaptureStopParams{Handle: "netcap-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestNetCapturePrecreatesPrivateFiles(t *testing.T) {
	for _, mask := range []int{0o022, 0o000} {
		t.Run(fmt.Sprintf("umask_%03o", mask), func(t *testing.T) {
			previous := syscall.Umask(mask)
			defer syscall.Umask(previous)
			baseDir := t.TempDir()
			if err := os.Chmod(baseDir, 0o711); err != nil {
				t.Fatal(err)
			}
			proc := &fakeNetCaptureProcess{done: make(chan error, 2)}
			m := newNetCaptureManager(func(context.Context, io.Writer, io.Writer, string, ...string) (netCaptureProcess, error) {
				return proc, nil
			}, baseDir, os.Geteuid)
			if m.ownerUID != os.Geteuid() {
				t.Fatalf("ownerUID = %d", m.ownerUID)
			}
			var outputs []string
			for range 2 {
				res, err := m.start(uint32(os.Getuid()), netCaptureStartParams{Interface: "en0"})
				if err != nil {
					t.Fatal(err)
				}
				output := res["output_path"].(string)
				outputs = append(outputs, output)
				info, err := os.Stat(output)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("precreated file = %v, %v", info, err)
				}
				proc.done <- nil
				if _, err := m.stop(uint32(os.Getuid()), netCaptureStopParams{Handle: res["handle"].(string)}); err != nil {
					t.Fatal(err)
				}
			}
			namePattern := regexp.MustCompile(`^netcap-[0-9]+-([0-9a-f]{32})\.pcap$`)
			first := namePattern.FindStringSubmatch(filepath.Base(outputs[0]))
			second := namePattern.FindStringSubmatch(filepath.Base(outputs[1]))
			if len(first) != 2 || len(second) != 2 || first[1] == second[1] {
				t.Fatalf("capture names lack distinct 32-hex suffixes: %v", outputs)
			}
			info, err := os.Stat(filepath.Join(baseDir, fmt.Sprint(os.Getuid())))
			if err != nil || info.Mode().Perm() != 0o711 {
				t.Fatalf("per-UID directory = %v, %v", info, err)
			}
		})
	}
}

func TestNetCaptureManagerUsesInjectedEUID(t *testing.T) {
	m := newNetCaptureManager(nil, t.TempDir(), func() int { return 12345 })
	if m.ownerUID != 12345 {
		t.Fatalf("ownerUID = %d, want 12345", m.ownerUID)
	}
}

func TestCaptureDirectoryModeIgnoresUmask(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "captures")
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	path := filepath.Join(baseDir, fmt.Sprint(os.Getuid()))
	if err := ensureCaptureDir(baseDir, path, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{baseDir, path} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o711 {
			t.Fatalf("directory %q = %v, %v", dir, info, err)
		}
	}
}

func TestPrecreateCaptureFileRejectsExistingPaths(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular.pcap")
	if err := os.WriteFile(regular, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := precreateCaptureFile(regular); err == nil {
		file.Close()
		t.Fatal("existing file was replaced")
	}
	link := filepath.Join(dir, "link.pcap")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if file, err := precreateCaptureFile(link); err == nil {
		file.Close()
		t.Fatal("symlink was accepted")
	}
	data, err := os.ReadFile(regular)
	if err != nil || string(data) != "existing" {
		t.Fatalf("target changed: %q, %v", data, err)
	}
}

func TestNetCaptureStartFailureRemovesPrecreatedFile(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Chmod(baseDir, 0o711); err != nil {
		t.Fatal(err)
	}
	m := newNetCaptureManager(func(context.Context, io.Writer, io.Writer, string, ...string) (netCaptureProcess, error) {
		return nil, fmt.Errorf("starter failed")
	}, baseDir)
	if _, err := m.start(uint32(os.Getuid()), netCaptureStartParams{Interface: "en0"}); err == nil {
		t.Fatal("expected start failure")
	}
	files, err := filepath.Glob(filepath.Join(baseDir, fmt.Sprint(os.Getuid()), "*.pcap"))
	if err != nil || len(files) != 0 {
		t.Fatalf("precreated files remain: %v, %v", files, err)
	}
}

func TestNetCaptureStartRejectsExistingRandomPath(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Chmod(baseDir, 0o711); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	outputDir := filepath.Join(baseDir, fmt.Sprint(uid))
	if err := ensureCaptureDir(baseDir, outputDir, os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(outputDir, "netcap-1-"+strings.Repeat("00", 16)+".pcap")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	m := newNetCaptureManager(func(context.Context, io.Writer, io.Writer, string, ...string) (netCaptureProcess, error) {
		called = true
		return nil, nil
	}, baseDir)
	m.random = bytes.NewReader(make([]byte, 16))
	if _, err := m.start(uid, netCaptureStartParams{Interface: "en0"}); err == nil || called {
		t.Fatalf("existing path accepted or starter called: %v, %v", err, called)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "existing" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}

func TestNetCaptureStopReturnsOutputSize(t *testing.T) {
	proc := &fakeNetCaptureProcess{done: make(chan error, 1)}
	baseDir := t.TempDir()
	if err := os.Chmod(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newNetCaptureManager(func(_ context.Context, _, _ io.Writer, _ string, _ ...string) (netCaptureProcess, error) {
		return proc, nil
	}, baseDir)
	m.ownerUID = os.Geteuid()

	res, err := m.start(uint32(os.Getuid()), netCaptureStartParams{Interface: "en0", DurationMS: 5000})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	output := res["output_path"].(string)
	if err := os.WriteFile(output, []byte("pcap"), 0o600); err != nil {
		t.Fatal(err)
	}
	proc.done <- nil

	stop, err := m.stop(uint32(os.Getuid()), netCaptureStopParams{Handle: "netcap-1"})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stop["size_bytes"] != int64(4) {
		t.Fatalf("stop = %+v, want size 4", stop)
	}
}

func TestNetCaptureRejectsUnsafeParams(t *testing.T) {
	called := false
	m := newNetCaptureManager(func(context.Context, io.Writer, io.Writer, string, ...string) (netCaptureProcess, error) {
		called = true
		return nil, nil
	}, t.TempDir())

	if _, err := m.start(501, netCaptureStartParams{
		Interface:  "en0;rm",
		DurationMS: int((time.Minute + time.Millisecond).Milliseconds()),
	}); err == nil {
		t.Fatal("expected invalid params error")
	}
	if called {
		t.Fatal("starter was called for invalid params")
	}
}

func TestNetCaptureStopUnknownHandle(t *testing.T) {
	m := newNetCaptureManager(nil, t.TempDir())
	if _, err := m.stop(501, netCaptureStopParams{Handle: "missing"}); err == nil {
		t.Fatal("expected unknown handle error")
	}
}

func TestNetCaptureStopRejectsOtherUID(t *testing.T) {
	proc := &fakeNetCaptureProcess{done: make(chan error, 1)}
	baseDir := t.TempDir()
	if err := os.Chmod(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := newNetCaptureManager(func(context.Context, io.Writer, io.Writer, string, ...string) (netCaptureProcess, error) {
		return proc, nil
	}, baseDir)
	m.ownerUID = os.Geteuid()
	uid := uint32(os.Getuid())
	if _, err := m.start(uid, netCaptureStartParams{Interface: "en0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.stop(uid+1, netCaptureStopParams{Handle: "netcap-1"}); err == nil {
		t.Fatal("another UID stopped the capture")
	}
	proc.done <- nil
	if _, err := m.stop(uid, netCaptureStopParams{Handle: "netcap-1"}); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFinalizationRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "netcap.pcap")
	if err := os.WriteFile(target, []byte("safe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(link, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		defer file.Close()
	}
	if err == nil {
		t.Fatal("symlink was accepted")
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("target changed: %v, %v", info, err)
	}
}

func TestCaptureFinalizationUsesOriginalDescriptor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.pcap")
	target := filepath.Join(dir, "target")
	file, err := precreateCaptureFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("pcap")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := makeCaptureReadableByOwner(file, uint32(os.Getuid()), os.Geteuid()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path + ".moved")
	if err != nil || string(data) != "pcap" {
		t.Fatalf("original bytes = %q, %v", data, err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "safe" {
		t.Fatalf("symlink target bytes = %q, %v", data, err)
	}
}

func TestCaptureDirectoryVerification(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCaptureDir(info, os.Geteuid()+1); err == nil {
		t.Fatal("foreign owner accepted")
	}
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{{0o711, true}, {0o755, true}, {0o775, false}, {0o757, false}, {0o733, false}} {
		if err := os.Chmod(dir, tc.mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyCaptureDir(info, os.Geteuid()); (err == nil) != tc.ok {
			t.Errorf("mode %04o: verification error = %v", tc.mode, err)
		}
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(file)
	if err != nil || verifyCaptureDir(info, os.Geteuid()) == nil {
		t.Fatalf("regular file accepted: %v, %v", info, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(link)
	if err != nil || verifyCaptureDir(info, os.Geteuid()) == nil {
		t.Fatalf("symlink accepted: %v, %v", info, err)
	}
}

func TestCaptureFinalizationRejectsWidenedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.pcap")
	if err := os.WriteFile(path, []byte("capture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := makeCaptureReadableByOwner(file, uint32(os.Getuid()), os.Geteuid()); err == nil {
		t.Fatal("widened mode was accepted")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed after rejected finalization: %v, %v", info, err)
	}
}
