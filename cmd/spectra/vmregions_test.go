package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const vrCLIOut = `REGION TYPE                    START - END         [ VSIZE  RSDNT  DIRTY   SWAP] PRT/MAX SHRMOD PURGE    REGION DETAIL
__TEXT                      102300000-102388000    [  544K   528K     0K     0K] r-x/r-x SM=COW          /bin/zsh
JIT region                  400000000-400100000    [ 1024K   512K   512K     0K] rwx/rwx SM=PRV
`

func vrRunnerReturning(out string, err error) vmregionsRunner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestRunVmregionsHuman(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runVmregionsWithIO([]string{"4012"}, &out, &errBuf, vrRunnerReturning(vrCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "RWX region") || !strings.Contains(s, "JIT region") {
		t.Errorf("expected RWX flag, got:\n%s", s)
	}
}

func TestRunVmregionsJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runVmregionsWithIO([]string{"--json", "4012"}, &out, &errBuf, vrRunnerReturning(vrCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"rwx_regions"`) || !strings.Contains(out.String(), `"total_dirty_bytes"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunVmregionsPermission(t *testing.T) {
	runner := vrRunnerReturning("vmmap: Unable to access process. Not permitted.\n", errors.New("exit status 1"))
	var out, errBuf bytes.Buffer
	if code := runVmregionsWithIO([]string{"4012"}, &out, &errBuf, runner); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "root") {
		t.Errorf("expected a root hint, got: %q", errBuf.String())
	}
}

func TestRunVmregionsArgValidation(t *testing.T) {
	runner := vrRunnerReturning(vrCLIOut, nil)
	for _, args := range [][]string{{}, {"notapid"}, {"0"}, {"1", "2"}, {"--top", "-1", "4012"}} {
		var out, errBuf bytes.Buffer
		if code := runVmregionsWithIO(args, &out, &errBuf, runner); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
