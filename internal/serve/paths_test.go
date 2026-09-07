package serve

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/hostos"
)

func TestDefaultSockPathLinuxXDGRuntime(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got, err := DefaultSockPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/run/user/1000/spectra/sock" {
		t.Errorf("sock path = %q, want /run/user/1000/spectra/sock", got)
	}
}

func TestDefaultSockPathLinuxFallback(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	t.Setenv("XDG_RUNTIME_DIR", "")
	got, err := DefaultSockPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(".spectra", "sock")) {
		t.Errorf("sock path = %q, want ~/.spectra/sock fallback", got)
	}
}

func TestDefaultLogPathLinuxState(t *testing.T) {
	defer hostos.SetForTest(hostos.Linux)()
	t.Setenv("XDG_STATE_HOME", "/home/u/.local/state")
	got, err := DefaultLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/home/u/.local/state/spectra/daemon.jsonl" {
		t.Errorf("log path = %q", got)
	}
}

func TestDefaultLogPathDarwin(t *testing.T) {
	defer hostos.SetForTest(hostos.Darwin)()
	got, err := DefaultLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, filepath.Join("Library", "Logs", "Spectra")) {
		t.Errorf("darwin log path = %q, want ~/Library/Logs/Spectra", got)
	}
}
