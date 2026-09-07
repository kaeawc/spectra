package main

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestInstallHelperLinuxRunsExpectedRootOperations(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.env["SUDO_USER"] = "alice"
	if err := installHelperLinux(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}

	helperSrc := filepath.Join(filepath.Dir(fake.executablePath), "spectra-helper")
	wantRuns := [][]string{
		{"groupadd", "-f", helperGroupLinux},
		{"usermod", "-aG", helperGroupLinux, "alice"},
		{"mkdir", "-p", filepath.Dir(helperBinaryDestLinux)},
		{"cp", helperSrc, helperBinaryDestLinux},
		{"chown", "root:root", helperBinaryDestLinux},
		{"chmod", "755", helperBinaryDestLinux},
		{"cp", "/tmp/spectra-helper-1", helperSystemdUnitPath},
		{"systemctl", "daemon-reload"},
		{"systemctl", "enable", "--now", "spectra-helper.service"},
	}
	if !reflect.DeepEqual(fake.runs, wantRuns) {
		t.Fatalf("runs = %v,\nwant %v", fake.runs, wantRuns)
	}
	if !slices.Equal(fake.tempContents, []string{helperSystemdUnitContent}) {
		t.Fatalf("temp contents = %q", fake.tempContents)
	}
}

func TestInstallHelperLinuxSkipsRootUser(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	fake.env["SUDO_USER"] = "root"
	if err := installHelperLinux(fake.deps(), helperInstallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, r := range fake.runs {
		if len(r) > 0 && r[0] == "usermod" {
			t.Errorf("should not usermod the root user; runs=%v", fake.runs)
		}
	}
}

func TestUninstallHelperLinux(t *testing.T) {
	fake := newFakeHelperInstallDeps(t)
	if err := uninstallHelperLinux(fake.deps()); err != nil {
		t.Fatal(err)
	}
	wantRuns := [][]string{
		{"systemctl", "disable", "--now", "spectra-helper.service"},
		{"rm", "-f", helperSystemdUnitPath},
		{"systemctl", "daemon-reload"},
		{"rm", "-f", helperBinaryDestLinux},
	}
	if !reflect.DeepEqual(fake.runs, wantRuns) {
		t.Fatalf("runs = %v,\nwant %v", fake.runs, wantRuns)
	}
}

func TestSudoCommandAllowedLinux(t *testing.T) {
	for _, ok := range []string{"groupadd", "usermod", "systemctl", "cp", "chown", "chmod", "mkdir", "rm"} {
		if !sudoCommandAllowedLinux(ok) {
			t.Errorf("%q should be allowlisted", ok)
		}
	}
	for _, bad := range []string{"dseditgroup", "launchctl", "bash", "sh", "rm -rf"} {
		if sudoCommandAllowedLinux(bad) {
			t.Errorf("%q must not be allowlisted", bad)
		}
	}
}
