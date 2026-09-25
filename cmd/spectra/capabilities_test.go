package main

import (
	"strings"
	"testing"
)

func TestCapabilitiesHumanAndInvalidArgs(t *testing.T) {
	out := captureStdout(t, func() {
		if code := dispatch([]string{"capabilities"}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
	for _, want := range []string{"NAME", "OUTPUT", "RESULT SCHEMA", "ARGV", "spectra.capabilities v1", "version", "inspect", "snapshot", "capabilities"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q: %s", want, out)
		}
	}
	for _, args := range [][]string{{"capabilities", "--unknown"}, {"capabilities", "extra"}} {
		if code := dispatch(args); code != 2 {
			t.Fatalf("dispatch(%v) = %d, want 2", args, code)
		}
	}
}
