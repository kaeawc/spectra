package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDaemonSystemdUnitContent(t *testing.T) {
	unit := daemonSystemdUnit("/usr/bin/spectra", []string{"serve", "--sock", "/run/user/1000/spectra/sock"})
	for _, want := range []string{
		"[Unit]",
		"Description=Spectra daemon",
		"[Service]",
		"ExecStart=/usr/bin/spectra serve --sock /run/user/1000/spectra/sock",
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestInstallDaemonUnitEnables(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	fake := newFakeDaemonAgentDeps(t)
	unitPath, err := installDaemonUnit(daemonAgentOptions{NoLogFile: true}, fake.deps())
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(fake.home, ".config", "systemd", "user", systemdDaemonUnit)
	if unitPath != wantPath {
		t.Errorf("unit path = %q, want %q", unitPath, wantPath)
	}
	if _, ok := fake.files[wantPath]; !ok {
		t.Errorf("unit file not written to %q", wantPath)
	}
	// Expect daemon-reload then enable --now.
	if !containsRun(fake.runs, []string{"daemon-reload"}) {
		t.Errorf("missing daemon-reload; runs=%v", fake.runs)
	}
	if !containsRun(fake.runs, []string{"enable", "--now", systemdDaemonUnit}) {
		t.Errorf("missing enable --now; runs=%v", fake.runs)
	}
}

func TestInstallDaemonUnitNoLoad(t *testing.T) {
	fake := newFakeDaemonAgentDeps(t)
	if _, err := installDaemonUnit(daemonAgentOptions{NoLoad: true, NoLogFile: true}, fake.deps()); err != nil {
		t.Fatal(err)
	}
	if len(fake.runs) != 0 {
		t.Errorf("NoLoad should not invoke systemctl; runs=%v", fake.runs)
	}
}

func TestUninstallDaemonUnit(t *testing.T) {
	fake := newFakeDaemonAgentDeps(t)
	if _, err := installDaemonUnit(daemonAgentOptions{NoLoad: true, NoLogFile: true}, fake.deps()); err != nil {
		t.Fatal(err)
	}
	unitPath, err := uninstallDaemonUnit(fake.deps())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.files[unitPath]; ok {
		t.Errorf("unit file still present after uninstall")
	}
	if !containsRun(fake.runs, []string{"disable", "--now", systemdDaemonUnit}) {
		t.Errorf("missing disable --now; runs=%v", fake.runs)
	}
}

func containsRun(runs [][]string, want []string) bool {
	for _, r := range runs {
		if slices.Equal(r, want) {
			return true
		}
	}
	return false
}
