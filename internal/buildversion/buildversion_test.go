package buildversion

import (
	"runtime/debug"
	"testing"
)

func infoWith(settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
	return func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: settings}, true
	}
}

func TestResolve(t *testing.T) {
	rev := debug.BuildSetting{Key: "vcs.revision", Value: "65cc800aa1b2c3d4e5f60718293a4b5c6d7e8f90"}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}
	tests := []struct {
		name    string
		stamped string
		read    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"ldflags tag wins", "v0.1.0", infoWith(rev, dirty), "v0.1.0"},
		{"prerelease tag wins", "v0.1.0-rc.1", infoWith(rev), "v0.1.0-rc.1"},
		{"dev with revision", Dev, infoWith(rev, clean), "dev+65cc800aa1b2"},
		{"empty stamp with revision", "", infoWith(rev), "dev+65cc800aa1b2"},
		{"dirty tree", Dev, infoWith(rev, dirty), "dev+65cc800aa1b2.dirty"},
		{"short revision kept whole", Dev, infoWith(debug.BuildSetting{Key: "vcs.revision", Value: "abc123"}), "dev+abc123"},
		{"no vcs info", Dev, infoWith(), Dev},
		{"no build info", Dev, func() (*debug.BuildInfo, bool) { return nil, false }, Dev},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve(tt.stamped, tt.read); got != tt.want {
				t.Fatalf("resolve(%q) = %q, want %q", tt.stamped, got, tt.want)
			}
		})
	}
}
