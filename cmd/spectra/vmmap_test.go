package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const vmmapCLIOut = `Physical footprint:         1665K
Physical footprint (peak):  1681K

REGION TYPE                        SIZE     SIZE     SIZE     SIZE
===========                     ======= ========    =====  =======
MALLOC_SMALL                      40.0M     304K     304K       0K
Stack                             8176K      32K      32K       0K
===========                     ======= ========    =====  =======
TOTAL                            929.0M   263.9M    1681K       0K
`

func vmmapRunnerReturning(out string, err error) vmmapRunner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), err }
}

func TestRunVmmapHuman(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runVmmapWithIO([]string{"4012"}, &out, &errBuf, vmmapRunnerReturning(vmmapCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "MALLOC_SMALL") || !strings.Contains(s, "footprint") || !strings.Contains(s, "TOTAL") {
		t.Errorf("expected composition summary, got:\n%s", s)
	}
}

func TestRunVmmapJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runVmmapWithIO([]string{"--json", "4012"}, &out, &errBuf, vmmapRunnerReturning(vmmapCLIOut, nil))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"regions"`) || !strings.Contains(out.String(), `"dirty_bytes"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunVmmapPermissionMessage(t *testing.T) {
	runner := vmmapRunnerReturning("vmmap: Unable to access process. Not permitted.\n", errors.New("exit status 1"))
	var out, errBuf bytes.Buffer
	code := runVmmapWithIO([]string{"4012"}, &out, &errBuf, runner)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errBuf.String(), "root") {
		t.Errorf("expected a root/sudo hint, got: %q", errBuf.String())
	}
}

func TestRunVmmapArgValidation(t *testing.T) {
	runner := vmmapRunnerReturning(vmmapCLIOut, nil)
	for _, args := range [][]string{{}, {"notapid"}, {"0"}, {"1", "2"}} {
		var out, errBuf bytes.Buffer
		if code := runVmmapWithIO(args, &out, &errBuf, runner); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
