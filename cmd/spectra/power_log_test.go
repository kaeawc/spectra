package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const plLogFixture = `2026-08-06 00:15:03 -0500 Sleep               	Entering Sleep state
2026-08-06 02:00:00 -0500 Wake                	Wake from Deep Idle due to EC.LidOpen`

const plAssertFixture = `   pid 335(powerd): [0x00043c8200019fc7] 05:14:01 PreventUserIdleSystemSleep named: "keep display on"`

func plRunner(logOut, assertOut string, fail bool) func(string, ...string) ([]byte, error) {
	return func(name string, args ...string) ([]byte, error) {
		if fail {
			return nil, errors.New("boom")
		}
		if len(args) >= 2 && args[1] == "log" {
			return []byte(logOut), nil
		}
		return []byte(assertOut), nil
	}
}

func TestRunPowerLogRenders(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runPowerLog(nil, &out, &errBuf, plRunner(plLogFixture, plAssertFixture, false)); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	for _, want := range []string{"Sleep/Wake events", "Wake", "Currently blocking sleep", "powerd"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q; got:\n%s", want, s)
		}
	}
}

func TestRunPowerLogNoBlockers(t *testing.T) {
	var out, errBuf bytes.Buffer
	runPowerLog(nil, &out, &errBuf, plRunner(plLogFixture, "", false))
	if !strings.Contains(out.String(), "Nothing is currently blocking sleep") {
		t.Errorf("expected no-blockers message; got:\n%s", out.String())
	}
}

func TestRunPowerLogJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runPowerLog([]string{"--json"}, &out, &errBuf, plRunner(plLogFixture, plAssertFixture, false)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"type": "PreventUserIdleSystemSleep"`) {
		t.Errorf("json missing blocker; got:\n%s", out.String())
	}
}

func TestRunPowerLogError(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runPowerLog(nil, &out, &errBuf, plRunner("", "", true)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}
