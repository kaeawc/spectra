package snapshot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

func buildWithJPSRunner(run jvm.CmdRunner) Snapshot {
	return Build(context.Background(), Options{
		SpectraVersion: "test",
		AppPaths:       []string{"/dev/null/__skip__"},
		SkipApps:       true,
		SkipProcesses:  true,
		SkipStorage:    true,
		SkipServices:   true,
		SkipUpdates:    true,
		JVMOpts:        jvm.CollectOptions{CmdRunner: run},
	})
}

func hasJVMWarning(warnings []string) bool {
	for _, w := range warnings {
		if strings.Contains(w, "jps") {
			return true
		}
	}
	return false
}

func TestBuildWarnsWhenJPSUnavailable(t *testing.T) {
	snap := buildWithJPSRunner(func(string, ...string) ([]byte, error) {
		return nil, fmt.Errorf("jps: not found")
	})
	if !hasJVMWarning(snap.Warnings) {
		t.Fatalf("expected a jps degradation warning, got %v", snap.Warnings)
	}
}

func TestBuildNoWarnWhenJPSAvailable(t *testing.T) {
	snap := buildWithJPSRunner(func(string, ...string) ([]byte, error) {
		return []byte(""), nil // jps ran, no JVMs
	})
	if hasJVMWarning(snap.Warnings) {
		t.Fatalf("unexpected jps warning when jps is available: %v", snap.Warnings)
	}
}

func TestBuildNoWarnWhenJVMsSkipped(t *testing.T) {
	snap := Build(context.Background(), Options{
		SpectraVersion: "test",
		AppPaths:       []string{"/dev/null/__skip__"},
		SkipApps:       true,
		SkipProcesses:  true,
		SkipStorage:    true,
		SkipServices:   true,
		SkipUpdates:    true,
		SkipJVMs:       true,
	})
	if hasJVMWarning(snap.Warnings) {
		t.Fatalf("no jvm warning expected when JVMs are skipped: %v", snap.Warnings)
	}
}
