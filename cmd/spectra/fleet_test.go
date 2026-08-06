package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/fleet"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/toolchain"
)

func fakeFleet() []fleet.HostSnapshot {
	return []fleet.HostSnapshot{
		{Hostname: "laptop", Snap: snapshot.Snapshot{
			Apps:       []detect.Result{{Path: "/Applications/Unsigned.app", TeamID: "", BundleID: "com.x.app", AppVersion: "2.1"}},
			Toolchains: toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 21, ReleaseString: "21.0.6"}}},
		}},
		{Hostname: "ci-mac", Snap: snapshot.Snapshot{
			Apps:       []detect.Result{{Path: "/Applications/Signed.app", TeamID: "TEAM1"}},
			Toolchains: toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 17, ReleaseString: "17.0.10"}}},
		}},
	}
}

func okLoader() ([]fleet.HostSnapshot, error) { return fakeFleet(), nil }

func TestRunFleetNoArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO(nil, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunFleetSymptom(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"symptom", "app-unsigned"}, &out, &errBuf, okLoader); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "firing (1): laptop") {
		t.Errorf("symptom output = %q", s)
	}
	if !strings.Contains(s, "clear  (1): ci-mac") {
		t.Errorf("symptom output = %q", s)
	}
}

func TestRunFleetDriftJDK(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift", "--jdk"}, &out, &errBuf, okLoader); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "21.0.6") || !strings.Contains(s, "17.0.10") {
		t.Errorf("jdk drift output = %q", s)
	}
}

func TestRunFleetDriftApp(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift", "--app", "com.x.app"}, &out, &errBuf, okLoader); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	s := out.String()
	if !strings.Contains(s, "laptop") || !strings.Contains(s, "2.1") || !strings.Contains(s, "absent") {
		t.Errorf("app drift output = %q", s)
	}
}

func TestRunFleetDriftNoDimension(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift"}, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunFleetEmptyStore(t *testing.T) {
	empty := func() ([]fleet.HostSnapshot, error) { return nil, nil }
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"symptom", "app-unsigned"}, &out, &errBuf, empty); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "no hosts") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunFleetLoadError(t *testing.T) {
	failing := func() ([]fleet.HostSnapshot, error) { return nil, errors.New("db locked") }
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift", "--jdk"}, &out, &errBuf, failing); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunFleetSymptomUnknownRule(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"symptom", "no-such-rule"}, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2 for unknown rule id", code)
	}
	if !strings.Contains(errBuf.String(), "unknown rule id") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestRunFleetDriftBothDimensions(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift", "--jdk", "--app", "com.x"}, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2 when both dimensions given", code)
	}
}

func TestRunFleetDriftStrayArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"drift", "--jdk", "extra"}, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2 for stray operands", code)
	}
}

func TestRunFleetUnknownSub(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runFleetWithIO([]string{"bogus"}, &out, &errBuf, okLoader); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
