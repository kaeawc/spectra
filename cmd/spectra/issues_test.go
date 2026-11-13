package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRunIssuesUpdateRejectsUnknownStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if code := runIssuesUpdate([]string{"--status", "resolved", "issue-1"}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	// Validation happens before the store is opened.
	if _, err := os.Stat(filepath.Join(home, ".spectra", "spectra.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store was opened for an invalid status: %v", err)
	}
}
