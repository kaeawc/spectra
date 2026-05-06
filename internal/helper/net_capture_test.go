package helper

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	proc := &fakeNetCaptureProcess{done: make(chan error, 1)}
	baseDir := t.TempDir()
	uid := uint32(os.Getuid())
	m := newNetCaptureManager(func(_ context.Context, _, _ io.Writer, name string, args ...string) (netCaptureProcess, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return proc, nil
	}, baseDir)

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
	output := filepath.Join(baseDir, fmt.Sprint(uid), "netcap-1.pcap")
	wantArgs := []string{"-i", "en0", "-n", "-s", "4096", "-w", output, "tcp", "and", "host", "api.example.com", "and", "port", "443"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("args = %v, want %v", gotArgs, wantArgs)
	}
	if res["handle"] != "netcap-1" || res["output_path"] != output {
		t.Fatalf("result = %+v", res)
	}

	proc.done <- nil
}

func TestNetCaptureStopReturnsOutputSize(t *testing.T) {
	proc := &fakeNetCaptureProcess{done: make(chan error, 1)}
	baseDir := t.TempDir()
	m := newNetCaptureManager(func(_ context.Context, _, _ io.Writer, _ string, _ ...string) (netCaptureProcess, error) {
		return proc, nil
	}, baseDir)

	res, err := m.start(uint32(os.Getuid()), netCaptureStartParams{Interface: "en0", DurationMS: 5000})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	output := res["output_path"].(string)
	if err := os.WriteFile(output, []byte("pcap"), 0o600); err != nil {
		t.Fatal(err)
	}
	proc.done <- nil

	stop, err := m.stop(netCaptureStopParams{Handle: "netcap-1"})
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
	if _, err := m.stop(netCaptureStopParams{Handle: "missing"}); err == nil {
		t.Fatal("expected unknown handle error")
	}
}
