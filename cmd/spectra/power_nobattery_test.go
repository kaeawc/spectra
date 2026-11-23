package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func TestPrintPowerStateNoBattery(t *testing.T) {
	no := false
	var out bytes.Buffer
	printPowerState(&out, sysinfo.PowerState{HasBattery: &no})
	if !strings.Contains(out.String(), "source:    AC power (no battery)") {
		t.Errorf("output = %q", out.String())
	}
}
