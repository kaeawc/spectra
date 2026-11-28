package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/contracttest"
	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/hostos"
)

// TestContractUnsupportedCases records what `spectra <cmd> --json` prints on
// Linux for every macOS-only command: the Rust port must print the same bytes
// (crates/contract/tests/unsupported.rs). Rewrite with -contract-update.
func TestContractUnsupportedCases(t *testing.T) {
	restore := hostos.SetForTest(hostos.Linux)
	defer restore()
	for name, d := range darwinOnlyCommands {
		c := contracttest.New(t, "unsupported", name)
		c.Input(t, map[string]string{"command": name, "feature": d.feature, "hint": d.hint, "os": "linux"})
		var stdout, stderr bytes.Buffer
		code, gated := gateDarwinOnly(name, []string{"--json"}, &stdout, &stderr)
		if !gated {
			t.Fatalf("%s was not gated on linux", name)
		}
		c.Expect(t, stdout.Bytes(), code)
	}
}

// TestContractUnsupportedTextCases records `spectra <cmd>` (no --json) on
// Linux through dispatch: nothing on stdout, one line on stderr
// (expected.stderr), exit 1. Replayed by crates/cli/tests/contract.rs.
func TestContractUnsupportedTextCases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	restore := hostos.SetForTest(hostos.Linux)
	defer restore()
	for name := range darwinOnlyCommands {
		c := contracttest.New(t, "unsupported-text", name)
		args := []string{name}
		c.Input(t, map[string]any{"args": args, "os": "linux"})
		var code int
		stdout, stderr := captureStdio(t, func() { code = dispatch(args) })
		c.Expect(t, stdout, code)
		expectStderr(t, c, stderr)
	}
}

// contractVersion is the version the cli/version case injects.
const contractVersion = "v0.0.0-contract"

// TestContractCLICases records `spectra help`, `spectra version` (with the
// version injected) and a bare `spectra` through dispatch. Replayed by
// crates/cli/tests/contract.rs.
func TestContractCLICases(t *testing.T) {
	t.Setenv("SPECTRA_DEBUG", "")
	orig := version
	version = contractVersion
	defer func() { version = orig }()
	cases := []struct {
		name  string
		input map[string]any
		args  []string
	}{
		{"help", map[string]any{"args": []string{"help"}}, []string{"help"}},
		{"version", map[string]any{"args": []string{"version"}, "version": contractVersion}, []string{"version"}},
		{"no-args", map[string]any{"args": []string{}}, []string{}},
	}
	for _, tc := range cases {
		c := contracttest.New(t, "cli", tc.name)
		c.Input(t, tc.input)
		var code int
		stdout, stderr := captureStdio(t, func() { code = dispatch(tc.args) })
		c.Expect(t, stdout, code)
		expectStderr(t, c, stderr)
	}
}

// TestContractCapabilitiesCases records `spectra capabilities` (JSON and text)
// for each OS with the version and architecture injected: the interface list
// differs by OS. Replayed by crates/cli/tests/contract.rs.
func TestContractCapabilitiesCases(t *testing.T) {
	orig := version
	version = contractVersion
	defer func() { version = orig }()
	for _, goos := range []string{"darwin", "linux"} {
		for _, asJSON := range []bool{true, false} {
			args := []string{"capabilities"}
			name := "text-" + goos
			if asJSON {
				args = append(args, "--json")
				name = "json-" + goos
			}
			c := contracttest.New(t, "capabilities", name)
			c.Input(t, map[string]any{"args": args, "os": goos, "arch": "arm64", "version": contractVersion})
			var stdout bytes.Buffer
			if err := writeCapabilities(&stdout, asJSON, goos, "arm64"); err != nil {
				t.Fatal(err)
			}
			c.Expect(t, stdout.Bytes(), 0)
		}
	}
}

// captureStdio runs fn with os.Stdout and os.Stderr redirected to pipes and
// returns what it wrote to each.
func captureStdio(t *testing.T, fn func()) (stdout, stderr []byte) {
	t.Helper()
	pipe := func(target **os.File) (restore func() []byte) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := *target
		*target = w
		done := make(chan []byte)
		go func() {
			b, _ := io.ReadAll(r)
			done <- b
		}()
		return func() []byte {
			*target = orig
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			b := <-done
			_ = r.Close()
			return b
		}
	}
	restoreOut := pipe(&os.Stdout)
	restoreErr := pipe(&os.Stderr)
	fn()
	return restoreOut(), restoreErr()
}

// expectStderr writes (or checks) expected.stderr beside the case's
// expected.stdout, following contracttest's -contract-update rule.
func expectStderr(t *testing.T, c contracttest.Case, stderr []byte) {
	t.Helper()
	path := filepath.Join(c.Dir, "expected.stderr")
	if *contracttest.Update {
		if err := fsutil.WriteFileAtomic(path, stderr, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path) // #nosec G304 -- path is under contract/cases
	if err != nil {
		t.Fatalf("%s: %v (write it with -contract-update)", path, err)
	}
	if !bytes.Equal(got, stderr) {
		t.Fatalf("%s no longer matches the Go build; rewrite it with -contract-update and port the change to Rust", path)
	}
}
