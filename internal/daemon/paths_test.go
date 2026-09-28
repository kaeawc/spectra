package daemon

import (
	"strings"
	"testing"
)

func TestDefaultPaths(t *testing.T) {
	home := func() (string, error) { return "/home/user", nil }
	for _, tc := range []struct {
		goos     string
		env      map[string]string
		dir, log string
	}{
		{"darwin", nil, "/home/user/.spectra", "/home/user/Library/Logs/Spectra/daemon.jsonl"},
		{"linux", nil, "/home/user/.spectra", "/home/user/.local/state/spectra/daemon.jsonl"},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/run/user/42", "XDG_STATE_HOME": "/state"}, "/run/user/42/spectra", "/state/spectra/daemon.jsonl"},
		{"linux", map[string]string{"SPECTRA_DAEMON_DIR": "/tmp/spd"}, "/tmp/spd", "/home/user/.local/state/spectra/daemon.jsonl"},
		{"darwin", map[string]string{"SPECTRA_DAEMON_LOG": "/tmp/daemon.jsonl"}, "/home/user/.spectra", "/tmp/daemon.jsonl"},
	} {
		p, err := DefaultPaths(func(k string) string { return tc.env[k] }, home, tc.goos)
		if err != nil {
			t.Fatal(err)
		}
		if p.Dir != tc.dir || p.Log != tc.log {
			t.Fatalf("%s: %+v", tc.goos, p)
		}
	}
	_, err := DefaultPaths(func(k string) string {
		if k == "SPECTRA_DAEMON_DIR" {
			return "/tmp/" + strings.Repeat("x", 100)
		}
		return ""
	}, home, "darwin")
	if err == nil {
		t.Fatal("long socket path accepted")
	}
}
