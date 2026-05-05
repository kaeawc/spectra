package main

import "testing"

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
