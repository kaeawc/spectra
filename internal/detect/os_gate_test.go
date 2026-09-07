package detect

import (
	"errors"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

// TestDetectUnsupportedOffDarwin verifies that app-bundle inspection is
// refused with a clear, errors.Is-detectable error on non-macOS hosts,
// before any macOS-only tool (plutil/otool/codesign) is invoked.
func TestDetectUnsupportedOffDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()

	_, err := DetectWith("/anything.app", Options{})
	if err == nil {
		t.Fatal("DetectWith on Linux returned nil error, want unsupported")
	}
	if !errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("error %v does not wrap hostos.ErrUnsupported", err)
	}
}

// TestDetectOptionOSOverridesHost confirms an explicit OS option wins
// over the detected host: forcing Darwin lets detection proceed past the
// gate even when the host resolver reports Linux.
func TestDetectOptionOSOverridesHost(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()

	// Path does not exist, so we expect the stat error, NOT the
	// unsupported error — proving the gate was passed.
	_, err := DetectWith("/nonexistent.app", Options{OS: hostos.Darwin})
	if err == nil {
		t.Fatal("expected a stat error for a nonexistent path")
	}
	if errors.Is(err, hostos.ErrUnsupported) {
		t.Fatalf("OS: Darwin should bypass the unsupported gate, got %v", err)
	}
}
