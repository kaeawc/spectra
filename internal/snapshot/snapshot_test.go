package snapshot

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/process"
)

func TestBuildHostOnly(t *testing.T) {
	// Use a sentinel non-existent path so collectApps does no real work.
	// Skip slow collectors (process list, ~/Library walk) for test speed.
	snap := Build(context.Background(), Options{
		SpectraVersion: "test",
		AppPaths:       []string{"/dev/null/__skip__"},
		SkipProcesses:  true,
		SkipStorage:    true,
		SkipJVMs:       true,
	})

	if snap.ID == "" {
		t.Error("ID empty")
	}
	if !strings.HasPrefix(snap.ID, "snap-") {
		t.Errorf("ID %q missing snap- prefix", snap.ID)
	}
	if snap.Kind != KindLive {
		t.Errorf("Kind = %q, want live", snap.Kind)
	}
	if time.Since(snap.TakenAt) > time.Minute {
		t.Errorf("TakenAt %v is suspiciously old", snap.TakenAt)
	}
	if snap.Host.OSName != "macOS" {
		t.Errorf("Host.OSName = %q", snap.Host.OSName)
	}
	if snap.Host.SpectraVersion != "test" {
		t.Errorf("Host.SpectraVersion = %q", snap.Host.SpectraVersion)
	}
	// The synthetic non-existent path should produce zero apps.
	if len(snap.Apps) != 0 {
		t.Errorf("expected 0 apps, got %d", len(snap.Apps))
	}
}

func TestNewIDUnique(t *testing.T) {
	a := newID()
	b := newID()
	if a == b {
		t.Errorf("newID() repeated: %q", a)
	}
}

func TestNewIDFormat(t *testing.T) {
	id := newID()
	// snap-YYYYMMDDTHHMMSSZ-NNNN
	parts := strings.Split(id, "-")
	if len(parts) != 3 {
		t.Fatalf("ID %q has %d parts, want 3", id, len(parts))
	}
	if parts[0] != "snap" {
		t.Errorf("part 0 = %q, want snap", parts[0])
	}
	if !strings.HasSuffix(parts[1], "Z") || len(parts[1]) != 16 {
		t.Errorf("part 1 = %q, want YYYYMMDDTHHMMSSZ", parts[1])
	}
	if len(parts[2]) != 4 {
		t.Errorf("part 2 = %q, want 4-digit suffix", parts[2])
	}
}

func TestScanAppsReadsDir(t *testing.T) {
	dir := t.TempDir()
	// Make a few fake bundle dirs and one non-bundle.
	for _, name := range []string{"Foo.app", "Bar.app", "ignore.txt"} {
		path := dir + "/" + name
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := scanApps(dir)
	if len(got) != 2 {
		t.Errorf("scanApps got %d, want 2 (Foo.app + Bar.app)", len(got))
	}
}

func TestBuildSkipAppsProducesNoApps(t *testing.T) {
	snap := Build(context.Background(), Options{
		SkipApps:      true,
		SkipProcesses: true,
		SkipStorage:   true,
		SkipJVMs:      true,
	})
	if snap.ID == "" {
		t.Error("ID should not be empty")
	}
	if snap.Host.Hostname == "" {
		t.Error("Host.Hostname should not be empty")
	}
	if len(snap.Apps) != 0 {
		t.Errorf("Apps = %d, want 0 when SkipApps=true", len(snap.Apps))
	}
}

func TestBuildAttributesProcessesToConfiguredAppPaths(t *testing.T) {
	psOut := "412 1 0.5 184320 204800 501 alice Sat May 2 22:40:05 2026 /Applications/Foo.app/Contents/MacOS/Foo --type=renderer\n" +
		"1 0 0.0 4096 8192 0 root Sat May 2 22:00:00 2026 /sbin/launchd\n"
	snap := Build(context.Background(), Options{
		AppPaths:    []string{"/Applications/Foo.app"},
		SkipApps:    true,
		SkipStorage: true,
		SkipJVMs:    true,
		ProcessOpts: process.CollectOptions{
			CmdRunner: func(name string, args ...string) ([]byte, error) {
				if name == "ps" {
					return []byte(psOut), nil
				}
				return nil, nil
			},
		},
	})

	if len(snap.Processes) != 2 {
		t.Fatalf("Processes len = %d, want 2", len(snap.Processes))
	}
	var matched bool
	for _, p := range snap.Processes {
		if p.PID == 412 {
			matched = true
			if p.AppPath != "/Applications/Foo.app" {
				t.Errorf("AppPath = %q, want /Applications/Foo.app", p.AppPath)
			}
		}
	}
	if !matched {
		t.Fatal("PID 412 not found")
	}
}
