package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunStorageCacheTriageUsesInjectedHome(t *testing.T) {
	home := t.TempDir()
	caches := filepath.Join(home, "Library", "Caches", "go-build")
	if err := os.MkdirAll(caches, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(caches, "a.o"), []byte("obj"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	var out, errBuf bytes.Buffer
	code := runStorageCacheTriage(nil, &out, &errBuf, func() (string, error) { return home, nil })
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "go-build") || !strings.Contains(s, "safe") {
		t.Errorf("expected the injected cache root to be triaged, got:\n%s", s)
	}
}

func TestRunStorageCacheTriageHomeError(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runStorageCacheTriage(nil, &out, &errBuf, func() (string, error) {
		return "", errors.New("no home")
	})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 on home-resolution failure", code)
	}
}

func TestRunStorageCacheTriageTooManyArgs(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runStorageCacheTriage([]string{"a", "b"}, &out, &errBuf, os.UserHomeDir)
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
