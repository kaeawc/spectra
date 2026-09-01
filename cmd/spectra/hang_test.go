package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

const hangLockSample = `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 main  (in App) + 1  [0x1]
        900 __psynch_mutexwait  (in libsystem_kernel.dylib) + 1  [0x2]
`

const hangIdleSample = `Call graph:
    900 Thread_2   DispatchQueue_1: com.apple.main-thread  (serial)
      900 CFRunLoopRunSpecific  (in CoreFoundation) + 1  [0x1]
        900 mach_msg2_trap  (in libsystem_kernel.dylib) + 1  [0x2]
`

func hangDepsFor(report string, capErr error, files map[string]string) hangDeps {
	return hangDeps{
		capture: func(context.Context, int, int) (string, error) { return report, capErr },
		readFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no such file")
		},
	}
}

func TestRunHangLockedExit3(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runHangWithIO([]string{"4012"}, &out, &errBuf, hangDepsFor(hangLockSample, nil, nil))
	if code != 3 {
		t.Fatalf("exit = %d, want 3 for a hung main thread", code)
	}
	if !strings.Contains(out.String(), "lock-blocked") {
		t.Errorf("expected lock-blocked verdict, got:\n%s", out.String())
	}
}

func TestRunHangIdleExit0(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runHangWithIO([]string{"4012"}, &out, &errBuf, hangDepsFor(hangIdleSample, nil, nil))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 for an idle main thread", code)
	}
	if !strings.Contains(out.String(), "idle") {
		t.Errorf("expected idle verdict, got:\n%s", out.String())
	}
}

func TestRunHangInput(t *testing.T) {
	deps := hangDepsFor("", nil, map[string]string{"/s.txt": hangLockSample})
	var out, errBuf bytes.Buffer
	if code := runHangWithIO([]string{"--input", "/s.txt"}, &out, &errBuf, deps); code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
}

func TestRunHangJSON(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runHangWithIO([]string{"--json", "4012"}, &out, &errBuf, hangDepsFor(hangIdleSample, nil, nil)); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"main_thread"`) || !strings.Contains(out.String(), `"verdict"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}

func TestRunHangCaptureError(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runHangWithIO([]string{"4012"}, &out, &errBuf, hangDepsFor("", errors.New("boom"), nil)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunHangArgValidation(t *testing.T) {
	deps := hangDepsFor(hangIdleSample, nil, map[string]string{"/s.txt": hangIdleSample})
	for _, args := range [][]string{{}, {"notapid"}, {"1", "2"}, {"--input", "/s.txt", "9"}} {
		var out, errBuf bytes.Buffer
		if code := runHangWithIO(args, &out, &errBuf, deps); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
