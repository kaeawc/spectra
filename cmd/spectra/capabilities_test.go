package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestCapabilitiesHumanAndInvalidArgs(t *testing.T) {
	out := captureStdout(t, func() {
		if code := dispatch([]string{"capabilities"}); code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	})
	for _, want := range []string{"NAME", "OUTPUT", "RESULT SCHEMA", "ARGV", "spectra.capabilities v1", "version", "snapshot", "capabilities"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q: %s", want, out)
		}
	}
	// App inspection is macOS-only, so the manifest advertises "inspect" only
	// on darwin (see capabilities.Build).
	if got := strings.Contains(out, "inspect"); got != (runtime.GOOS == "darwin") {
		t.Fatalf("inspect advertised = %v on %s: %s", got, runtime.GOOS, out)
	}
	for _, args := range [][]string{{"capabilities", "--unknown"}, {"capabilities", "extra"}} {
		if code := dispatch(args); code != 2 {
			t.Fatalf("dispatch(%v) = %d, want 2", args, code)
		}
	}
}
