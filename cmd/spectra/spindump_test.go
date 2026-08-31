package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// testDeps builds spindumpDeps whose runner is the given func and whose
// filesystem is served from an in-memory map.
func testDeps(run spindumpRunner, files map[string]string, written map[string]string) spindumpDeps {
	return spindumpDeps{
		run: run,
		readFile: func(path string) ([]byte, error) {
			if v, ok := files[path]; ok {
				return []byte(v), nil
			}
			return nil, os.ErrNotExist
		},
		writeFile: func(path string, data []byte, _ os.FileMode) error {
			if written != nil {
				written[path] = string(data)
			}
			return nil
		},
	}
}

const cliReport = `Duration:         2.0s

Process:          Foo [4012]
  100  Thread_1
    100  main + 1 (Foo + 1) [0x1]
      90  work + 2 (Foo + 2) [0x2]
`

func TestRunSpindumpInput(t *testing.T) {
	deps := testDeps(failRunner, map[string]string{"/reports/report.txt": cliReport}, nil)
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"--input", "/reports/report.txt"}, &out, &errBuf, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Foo [4012]") || !strings.Contains(out.String(), "work") {
		t.Errorf("expected parsed summary, got:\n%s", out.String())
	}
}

func TestRunSpindumpCaptureWritesOut(t *testing.T) {
	written := map[string]string{}
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		return []byte(cliReport), nil
	}
	deps := testDeps(runner, nil, written)
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"--duration", "1", "--out", "/tmp/raw.txt", "4012"}, &out, &errBuf, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "Foo [4012]") {
		t.Errorf("expected captured summary, got:\n%s", out.String())
	}
	if written["/tmp/raw.txt"] != cliReport {
		t.Errorf("--out did not save the raw report: %q", written["/tmp/raw.txt"])
	}
}

func TestRunSpindumpRootErrorMessage(t *testing.T) {
	runner := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("spindump must be run as root when sampling the live system\n"), errors.New("exit status 1")
	}
	var out, errBuf bytes.Buffer
	code := runSpindumpWithIO([]string{"4012"}, &out, &errBuf, testDeps(runner, nil, nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "--sudo") {
		t.Errorf("expected a --sudo hint on the root error, got: %q", errBuf.String())
	}
}

func TestRunSpindumpSudoUsesAbsolutePaths(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(cliReport), nil
	}
	var out, errBuf bytes.Buffer
	if code := runSpindumpWithIO([]string{"--sudo", "4012"}, &out, &errBuf, testDeps(runner, nil, nil)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if gotName != sudoBin || len(gotArgs) == 0 || gotArgs[0] != spindumpBin {
		t.Errorf("expected `%s %s ...`, got name=%q args=%v", sudoBin, spindumpBin, gotName, gotArgs)
	}
}

func TestRunSpindumpRejectsOutWithInput(t *testing.T) {
	deps := testDeps(failRunner, map[string]string{"/f": cliReport}, map[string]string{})
	var out, errBuf bytes.Buffer
	if code := runSpindumpWithIO([]string{"--input", "/f", "--out", "/o"}, &out, &errBuf, deps); code != 2 {
		t.Fatalf("exit = %d, want 2 for --out with --input", code)
	}
}

func TestRunSpindumpArgValidation(t *testing.T) {
	deps := testDeps(failRunner, nil, nil)
	cases := [][]string{
		{},                        // no pid, no input
		{"notapid"},               // bad pid
		{"--input", "/f", "4012"}, // both input and pid
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runSpindumpWithIO(args, &out, &errBuf, deps); code != 2 && code != 1 {
			t.Errorf("args %v: exit = %d, want non-zero usage/error", args, code)
		}
	}
}

func failRunner(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("runner should not be called")
}
