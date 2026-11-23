package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestGateDarwinOnlyJSON(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	var out, errBuf bytes.Buffer
	code, gated := gateDarwinOnly("vmmap", []string{"--json", "123"}, &out, &errBuf)
	if !gated || code != 1 {
		t.Fatalf("gated=%v code=%d, want gated with exit 1", gated, code)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q", out.String())
	}
	want := map[string]string{
		"command": "vmmap",
		"error":   "unsupported_platform",
		"feature": "vmmap memory composition",
		"os":      "linux",
		"message": "vmmap memory composition: unsupported on host OS (linux)",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["hint"] == "" {
		t.Error("vmmap should point at the Linux alternative")
	}
	if errBuf.Len() != 0 {
		t.Errorf("stderr = %q, want nothing in JSON mode", errBuf.String())
	}
}

func TestGateDarwinOnlyText(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	var out, errBuf bytes.Buffer
	code, gated := gateDarwinOnly("schedule", []string{"install"}, &out, &errBuf)
	if !gated || code != 1 || out.Len() != 0 {
		t.Fatalf("gated=%v code=%d stdout=%q", gated, code, out.String())
	}
	if s := errBuf.String(); !strings.Contains(s, "unsupported on host OS (linux)") || !strings.Contains(s, "systemd timer") {
		t.Errorf("stderr = %q", s)
	}
}

func TestGateDarwinOnlyPassesThrough(t *testing.T) {
	var out, errBuf bytes.Buffer
	func() {
		defer hostos.SetForTest(hostos.Darwin)()
		if _, gated := gateDarwinOnly("vmmap", nil, &out, &errBuf); gated {
			t.Error("vmmap gated on macOS")
		}
	}()
	defer hostos.SetForTest(hostos.Linux)()
	for _, cmd := range []string{"load", "process", "memory", "orphans", "jvm"} {
		if _, gated := gateDarwinOnly(cmd, []string{"--json"}, &out, &errBuf); gated {
			t.Errorf("%s gated on Linux", cmd)
		}
	}
	if _, gated := gateDarwinOnly("inspect", []string{"-h"}, &out, &errBuf); gated {
		t.Error("inspect -h gated; help should still print")
	}
	if out.Len()+errBuf.Len() != 0 {
		t.Errorf("pass-through wrote output: %q %q", out.String(), errBuf.String())
	}
}

func TestDispatchGatesDefaultInspect(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	if code := dispatch([]string{"/Applications/Slack.app"}); code != 1 {
		t.Errorf("dispatch(app path) = %d, want 1", code)
	}
}

func TestDarwinOnlyCommandsExist(t *testing.T) {
	names := map[string]bool{}
	for _, sc := range subcommandList() {
		names[sc.name] = true
	}
	for name := range darwinOnlyCommands {
		if !names[name] {
			t.Errorf("darwinOnlyCommands lists %q, which is not a subcommand", name)
		}
	}
}
