package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/toolchain"
)

func driftPair() toolchainPair {
	return toolchainPair{
		FromName: "my-mac",
		ToName:   "ci-mac-1",
		FromTC:   toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 17, ReleaseString: "17.0.10"}}},
		ToTC: toolchain.Toolchains{JDKs: []toolchain.JDKInstall{
			{VersionMajor: 17, ReleaseString: "17.0.10"},
			{VersionMajor: 21, ReleaseString: "21.0.6", Vendor: "Temurin", Source: "brew"},
		}},
	}
}

func TestRunReconcileRenders(t *testing.T) {
	load := func(from, target string) (toolchainPair, error) { return driftPair(), nil }
	var out, errBuf bytes.Buffer
	if code := runReconcileWithIO([]string{"ci-mac-1"}, &out, &errBuf, load); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "DRY RUN") || !strings.Contains(s, "make my-mac match ci-mac-1") {
		t.Errorf("missing DRY RUN header; got:\n%s", s)
	}
	if !strings.Contains(s, "JDK 21") {
		t.Errorf("expected the JDK 21 install step; got:\n%s", s)
	}
	// steps must be descriptions, not runnable commands
	if strings.Contains(s, "brew install temurin@21") {
		t.Errorf("plan should not emit a runnable command; got:\n%s", s)
	}
}

func TestRunReconcileIdentical(t *testing.T) {
	same := toolchain.Toolchains{JDKs: []toolchain.JDKInstall{{VersionMajor: 21, ReleaseString: "21.0.6"}}}
	load := func(from, target string) (toolchainPair, error) {
		return toolchainPair{FromName: "a", ToName: "b", FromTC: same, ToTC: same}, nil
	}
	var out, errBuf bytes.Buffer
	runReconcileWithIO([]string{"b"}, &out, &errBuf, load)
	if !strings.Contains(out.String(), "already matches") {
		t.Errorf("expected already-matches message; got:\n%s", out.String())
	}
}

func TestRunReconcileNoArgs(t *testing.T) {
	load := func(from, target string) (toolchainPair, error) { return driftPair(), nil }
	var out, errBuf bytes.Buffer
	if code := runReconcileWithIO(nil, &out, &errBuf, load); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestRunReconcileLoadError(t *testing.T) {
	load := func(from, target string) (toolchainPair, error) {
		return toolchainPair{}, errors.New("no stored host named \"ci-mac-1\"")
	}
	var out, errBuf bytes.Buffer
	if code := runReconcileWithIO([]string{"ci-mac-1"}, &out, &errBuf, load); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunReconcileJSON(t *testing.T) {
	load := func(from, target string) (toolchainPair, error) { return driftPair(), nil }
	var out, errBuf bytes.Buffer
	runReconcileWithIO([]string{"--json", "ci-mac-1"}, &out, &errBuf, load)
	if !strings.Contains(out.String(), `"category": "jdk"`) {
		t.Errorf("json missing steps; got:\n%s", out.String())
	}
}
