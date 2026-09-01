package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const lsmpCLIOut = `  name      ipc-object  rights
0x107  0x1a  recv
0x20b  0x2b  send
0x30c  0x3c  send
`

func lsmpRunnerReturning(out string, err error) lsmpRunner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestRunLsmpHuman(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runLsmpWithIO([]string{"4012"}, &out, &errBuf, lsmpRunnerReturning(lsmpCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "total ports: 3") || !strings.Contains(s, "send:      2") {
		t.Errorf("expected port summary, got:\n%s", s)
	}
}

func TestRunLsmpJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runLsmpWithIO([]string{"--json", "4012"}, &out, &errBuf, lsmpRunnerReturning(lsmpCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"send_rights"`) || !strings.Contains(out.String(), `"total_ports"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunLsmpPermission(t *testing.T) {
	runner := lsmpRunnerReturning("warning: should run as root for best output.\ntask_for_pid() failed: (null)\n", nil)
	var out, errBuf bytes.Buffer
	code := runLsmpWithIO([]string{"4012"}, &out, &errBuf, runner)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "--sudo") {
		t.Errorf("expected a --sudo hint, got: %q", errBuf.String())
	}
}

func TestRunLsmpEmptyReport(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runLsmpWithIO([]string{"4012"}, &out, &errBuf, lsmpRunnerReturning("  name  ipc-object  rights\n", nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a report with no ports", code)
	}
}

func TestRunLsmpSudoUsesAbsolutePaths(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte(lsmpCLIOut), nil
	}
	var out, errBuf bytes.Buffer
	if code := runLsmpWithIO([]string{"--sudo", "4012"}, &out, &errBuf, runner); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if gotName != lsmpSudoBin || len(gotArgs) == 0 || gotArgs[0] != lsmpBin {
		t.Errorf("expected `%s %s ...`, got name=%q args=%v", lsmpSudoBin, lsmpBin, gotName, gotArgs)
	}
}

func TestRunLsmpArgValidation(t *testing.T) {
	runner := lsmpRunnerReturning(lsmpCLIOut, nil)
	for _, args := range [][]string{{}, {"notapid"}, {"0"}, {"1", "2"}} {
		var out, errBuf bytes.Buffer
		if code := runLsmpWithIO(args, &out, &errBuf, runner); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunLsmpRunnerError(t *testing.T) {
	runner := lsmpRunnerReturning("", errors.New("lsmp -p: exit status 1"))
	var out, errBuf bytes.Buffer
	if code := runLsmpWithIO([]string{"4012"}, &out, &errBuf, runner); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
