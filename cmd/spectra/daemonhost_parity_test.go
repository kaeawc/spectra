package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/daemonhost"
	"github.com/kaeawc/spectra/internal/sysinfo"
)

func indented(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The daemon's power.current and thermal.current payloads must encode to the
// same bytes the CLI's --json prints for the same input.
func TestDaemonHostPowerMatchesCLIJSON(t *testing.T) {
	deps := fakePowerDeps()
	var cli, errb bytes.Buffer
	if rc := runPowerWithIO([]string{"--json"}, &cli, &errb, deps); rc != 0 {
		t.Fatalf("exit %d: %s", rc, errb.String())
	}
	got, err := daemonhost.PowerFrom(func(context.Context) sysinfo.PowerState { return deps.collect() },
		func() time.Time { return deps.clock() })(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := indented(t, got); cli.String() != want {
		t.Fatalf("daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", cli.String(), want)
	}
}

func TestDaemonHostThermalMatchesCLIJSON(t *testing.T) {
	for _, mk := range []func() string{func() string { return "50000" }, func() string { return "96000" }} {
		var cli, errb bytes.Buffer
		runThermalWithIO(context.Background(), []string{"--json"}, &cli, &errb, linuxZone(mk()))
		got, err := daemonhost.DefaultThermal(linuxZone(mk()))(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if want := indented(t, got); cli.String() != want {
			t.Fatalf("daemon payload differs from CLI JSON\ncli:\n%s\ndaemon:\n%s", cli.String(), want)
		}
	}
}
