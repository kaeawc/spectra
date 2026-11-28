package main

import (
	"bytes"
	"testing"

	"github.com/kaeawc/spectra/internal/contracttest"
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
