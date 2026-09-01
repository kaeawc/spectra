package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const mhAllBySize = `Malloc stack logging: recording
16 calls for 262144 bytes: start | main | bar | operator new
8 calls for 131072 bytes: start | main | foo | malloc
`

func mhRunner(out string, err error) mallocHistoryRunner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestRunMallocHistoryAllBySize(t *testing.T) {
	var gotArgs []string
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(mhAllBySize), nil
	}
	var out, errBuf bytes.Buffer
	code := runMallocHistoryWithIO([]string{"4012"}, &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	// malloc_history requires the pid before the mode: `<pid> -allBySize`.
	if len(gotArgs) != 2 || gotArgs[0] != "4012" || gotArgs[1] != "-allBySize" {
		t.Errorf("arg order = %v, want [4012 -allBySize]", gotArgs)
	}
	if !strings.Contains(out.String(), "operator new") || !strings.Contains(out.String(), "256 KB") {
		t.Errorf("expected top-site summary, got:\n%s", out.String())
	}
}

func TestRunMallocHistoryAddressArgOrder(t *testing.T) {
	var gotArgs []string
	runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("ALLOC 0x1: a | b\n"), nil
	}
	var out, errBuf bytes.Buffer
	if code := runMallocHistoryWithIO([]string{"--address", "0x1", "4012"}, &out, &errBuf, runner); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "4012" || gotArgs[1] != "0x1" {
		t.Errorf("arg order = %v, want [4012 0x1]", gotArgs)
	}
}

func TestRunMallocHistoryAddress(t *testing.T) {
	runner := mhRunner("ALLOC 0x600000abcdef [size=17]: start | main | leak | malloc\n", nil)
	var out, errBuf bytes.Buffer
	code := runMallocHistoryWithIO([]string{"--address", "0x600000abcdef", "4012"}, &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "ALLOC") || !strings.Contains(out.String(), "malloc") {
		t.Errorf("expected address backtrace, got:\n%s", out.String())
	}
}

func TestRunMallocHistoryStackLoggingDisabled(t *testing.T) {
	runner := mhRunner("malloc_history: stack logging not enabled for this process\n", errors.New("exit status 1"))
	var out, errBuf bytes.Buffer
	code := runMallocHistoryWithIO([]string{"4012"}, &out, &errBuf, runner)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "MallocStackLogging") {
		t.Errorf("expected the precondition message, got: %q", errBuf.String())
	}
}

func TestRunMallocHistoryJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runMallocHistoryWithIO([]string{"--json", "4012"}, &out, &errBuf, mhRunner(mhAllBySize, nil))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"sites"`) || !strings.Contains(out.String(), `"bytes"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunMallocHistorySudoAbsolutePaths(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(mhAllBySize), nil
	}
	var out, errBuf bytes.Buffer
	if code := runMallocHistoryWithIO([]string{"--sudo", "4012"}, &out, &errBuf, runner); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if gotName != mallocSudoBin || len(gotArgs) == 0 || gotArgs[0] != mallocHistoryBin {
		t.Errorf("expected `%s %s ...`, got name=%q args=%v", mallocSudoBin, mallocHistoryBin, gotName, gotArgs)
	}
}

func TestRunMallocHistoryArgValidation(t *testing.T) {
	runner := mhRunner(mhAllBySize, nil)
	for _, args := range [][]string{
		{},                              // no pid
		{"notapid"},                     // bad pid
		{"1", "2"},                      // too many
		{"--address", "nothex", "4012"}, // bad address
		{"--top", "-1", "4012"},         // bad top
	} {
		var out, errBuf bytes.Buffer
		if code := runMallocHistoryWithIO(args, &out, &errBuf, runner); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
