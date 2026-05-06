package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestHelperInstallUserPrefersSudoUser(t *testing.T) {
	env := map[string]string{"SUDO_USER": "alice", "USER": "root"}
	got := helperInstallUser(func(key string) string { return env[key] })
	if got != "alice" {
		t.Fatalf("helperInstallUser = %q, want alice", got)
	}
}

func TestHelperInstallUserFallsBackToUser(t *testing.T) {
	env := map[string]string{"USER": "bob"}
	got := helperInstallUser(func(key string) string { return env[key] })
	if got != "bob" {
		t.Fatalf("helperInstallUser = %q, want bob", got)
	}
}

func TestHelperNewsyslogContent(t *testing.T) {
	want := "/var/log/spectra-helper.log\troot:wheel\t640\t7\t1024\t*\tJ\n"
	if helperNewsyslogContent != want {
		t.Fatalf("helperNewsyslogContent = %q, want %q", helperNewsyslogContent, want)
	}
}

func TestInstallRootTextFileCopiesTemporaryContent(t *testing.T) {
	var gotPattern, gotContent string
	cleaned := false
	write := func(pattern, content string) (string, func(), error) {
		gotPattern = pattern
		gotContent = content
		return "/tmp/spectra-helper-test", func() { cleaned = true }, nil
	}

	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := installRootTextFile("/etc/newsyslog.d/spectra-helper.conf", helperNewsyslogContent, run, write); err != nil {
		t.Fatal(err)
	}
	if gotPattern != "spectra-helper-*" {
		t.Fatalf("pattern = %q", gotPattern)
	}
	if gotContent != helperNewsyslogContent {
		t.Fatalf("content = %q", gotContent)
	}
	if !cleaned {
		t.Fatal("temporary file cleanup was not called")
	}
	wantCalls := [][]string{{"cp", "/tmp/spectra-helper-test", "/etc/newsyslog.d/spectra-helper.conf"}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
}

func TestSudoCommandAllowedCoversHelperManagementCommands(t *testing.T) {
	for _, name := range []string{"chmod", "chown", "cp", "dseditgroup", "launchctl", "mkdir", "rm"} {
		if !sudoCommandAllowed(name) {
			t.Fatalf("sudoCommandAllowed(%q) = false", name)
		}
	}
}

func TestSudoRunRejectsNonAllowlistedCommand(t *testing.T) {
	err := sudoRun("sh", "-c", "echo should-not-run")
	if err == nil {
		t.Fatal("sudoRun accepted a non-allowlisted command")
	}
	if !strings.Contains(err.Error(), `sudo command "sh" is not allowlisted`) {
		t.Fatalf("error = %v", err)
	}
}
