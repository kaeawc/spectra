package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliReport = `Duration:         2.0s

Process:          Foo [4012]
  100  Thread_1
    100  main + 1 (Foo + 1) [0x1]
      90  work + 2 (Foo + 2) [0x2]
`

func TestRunSpindumpInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte(cliReport), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"--input", path}, &out, &errBuf, failRunner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Foo [4012]") || !strings.Contains(out.String(), "work") {
		t.Errorf("expected parsed summary, got:\n%s", out.String())
	}
}

func TestRunSpindumpCapture(t *testing.T) {
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte(cliReport), nil
	}
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"--duration", "1", "4012"}, &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Foo [4012]") {
		t.Errorf("expected captured summary, got:\n%s", out.String())
	}
}

func TestRunSpindumpRootErrorMessage(t *testing.T) {
	runner := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("spindump must be run as root when sampling the live system\n"), errors.New("exit status 1")
	}
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"4012"}, &out, &errBuf, runner)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "--sudo") {
		t.Errorf("expected a --sudo hint on the root error, got: %q", errBuf.String())
	}
}

func TestRunSpindumpSudoPrefixesCommand(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(cliReport), nil
	}
	var out, errBuf bytes.Buffer
	if code := runSpindumpWithIO([]string{"--sudo", "4012"}, &out, &errBuf, runner); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if gotName != "sudo" || len(gotArgs) == 0 || gotArgs[0] != "spindump" {
		t.Errorf("expected `sudo spindump ...`, got name=%q args=%v", gotName, gotArgs)
	}
}

func TestRunSpindumpArgValidation(t *testing.T) {
	cases := [][]string{
		{},                        // no pid, no input
		{"notapid"},               // bad pid
		{"--input", "/f", "4012"}, // both input and pid
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runSpindumpWithIO(args, &out, &errBuf, failRunner); code != 2 && code != 1 {
			t.Errorf("args %v: exit = %d, want non-zero usage/error", args, code)
		}
	}
}

func failRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("runner should not be called")
}
