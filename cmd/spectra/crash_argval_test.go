package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func okRead(string) ([]byte, error) { return []byte("{}\n{}"), nil }

func TestCrashInspectRejectsExtraPaths(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashInspect([]string{"a.ips", "b.ips"}, &out, &errBuf, okRead); code != 2 {
		t.Fatalf("exit = %d, want 2 for two positional paths", code)
	}
}

func TestCrashResourceRejectsExtraPaths(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashResource([]string{"a.ips", "b.ips"}, &out, &errBuf, okRead); code != 2 {
		t.Fatalf("exit = %d, want 2 for two positional paths", code)
	}
}

func TestCrashInspectReadFailure(t *testing.T) {
	fail := func(string) ([]byte, error) { return nil, errors.New("boom") }
	var out, errBuf bytes.Buffer
	code := runCrashInspect([]string{"missing.ips"}, &out, &errBuf, fail)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 on read failure", code)
	}
	if !strings.Contains(errBuf.String(), "boom") {
		t.Errorf("stderr = %q, want the read error surfaced", errBuf.String())
	}
}

func TestCrashResourceReadFailure(t *testing.T) {
	fail := func(string) ([]byte, error) { return nil, errors.New("boom") }
	var out, errBuf bytes.Buffer
	if code := runCrashResource([]string{"missing.ips"}, &out, &errBuf, fail); code != 1 {
		t.Fatalf("exit = %d, want 1 on read failure", code)
	}
}
