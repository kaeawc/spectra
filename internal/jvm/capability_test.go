package jvm

import (
	"testing"

	"github.com/kaeawc/spectra/internal/diag"
)

func TestDiagnosticsMatrixJDK25VirtualThreadCapabilities(t *testing.T) {
	matrix := DiagnosticsMatrix("25.0.2")

	if matrix.Architecture != "jvm" {
		t.Fatalf("Architecture = %q, want jvm", matrix.Architecture)
	}
	got := capabilityByID(matrix.Capabilities, "jvm.thread_dump.virtual")
	if got == nil {
		t.Fatal("virtual-thread dump capability missing")
	}
	if got.Status != diag.CapabilityAvailable {
		t.Fatalf("virtual-thread dump status = %q, want available", got.Status)
	}
	if len(got.Command) < 4 || got.Command[2] != "Thread.dump_to_file" {
		t.Fatalf("virtual-thread dump command = %v", got.Command)
	}

	jfr := capabilityByID(matrix.Capabilities, "jvm.jfr.virtual_threads")
	if jfr == nil || jfr.Status != diag.CapabilityAvailable {
		t.Fatalf("JFR virtual-thread capability = %#v, want available", jfr)
	}
}

func TestDiagnosticsProviderImplementsGenericInterface(t *testing.T) {
	var provider diag.Provider = DiagnosticsProvider{Version: "24.0.2"}
	matrix := provider.DiagnosticsMatrix()
	if matrix.Architecture != "jvm" || matrix.Version != "24.0.2" {
		t.Fatalf("matrix = %#v", matrix)
	}
}

func TestDiagnosticsMatrixPreJDK21VirtualThreadsUnavailable(t *testing.T) {
	matrix := DiagnosticsMatrix("17.0.10")
	got := capabilityByID(matrix.Capabilities, "jvm.thread_dump.virtual")
	if got == nil {
		t.Fatal("virtual-thread dump capability missing")
	}
	if got.Status != diag.CapabilityUnavailable {
		t.Fatalf("virtual-thread dump status = %q, want unavailable", got.Status)
	}
}

func TestParseJavaMajor(t *testing.T) {
	tests := map[string]int{
		"1.8.0_402":    8,
		"21.0.6+7-LTS": 21,
		"24":           24,
		"25.0.2":       25,
		"bad-version":  0,
		"":             0,
	}
	for version, want := range tests {
		if got := parseJavaMajor(version); got != want {
			t.Fatalf("parseJavaMajor(%q) = %d, want %d", version, got, want)
		}
	}
}

func capabilityByID(capabilities []diag.Capability, id string) *diag.Capability {
	for i := range capabilities {
		if capabilities[i].ID == id {
			return &capabilities[i]
		}
	}
	return nil
}
