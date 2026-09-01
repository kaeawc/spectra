package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

const fgSample = `Call graph:
    100 Thread_1   main-thread
      100 start  (in dyld) + 1  [0x1]
        100 main  (in App) + 2  [0x2]
          100 work  (in App) + 3  [0x3]
`

func fgDeps(report string, captureErr error, files, written map[string]string) flamegraphDeps {
	return flamegraphDeps{
		capture: func(context.Context, int, int) (string, error) { return report, captureErr },
		readFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, errors.New("no such file")
		},
		writeFile: func(p string, data []byte, _ os.FileMode) error {
			if written != nil {
				written[p] = string(data)
			}
			return nil
		},
	}
}

func TestRunFlamegraphCapture(t *testing.T) {
	written := map[string]string{}
	var out, errBuf bytes.Buffer
	code := runFlamegraphWithIO([]string{"4012"}, &out, &errBuf, fgDeps(fgSample, nil, nil, written))
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	svg := written["4012.flamegraph.svg"]
	if !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, "work") {
		t.Errorf("expected an SVG with the work frame, got:\n%s", svg)
	}
	if !strings.Contains(out.String(), "folded stacks") {
		t.Errorf("expected a summary line, got: %q", out.String())
	}
}

func TestRunFlamegraphInput(t *testing.T) {
	written := map[string]string{}
	deps := fgDeps("", nil, map[string]string{"/s.txt": fgSample}, written)
	var out, errBuf bytes.Buffer
	code := runFlamegraphWithIO([]string{"--input", "/s.txt", "--out", "/g.svg"}, &out, &errBuf, deps)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.HasPrefix(written["/g.svg"], "<svg") {
		t.Errorf("expected SVG at /g.svg, got: %q", written["/g.svg"])
	}
}

func TestRunFlamegraphCaptureError(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runFlamegraphWithIO([]string{"4012"}, &out, &errBuf, fgDeps("", errors.New("sample failed"), nil, nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRunFlamegraphNoStacks(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := runFlamegraphWithIO([]string{"4012"}, &out, &errBuf, fgDeps("no call graph here\n", nil, nil, nil))
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for a sample with no stacks", code)
	}
	if !strings.Contains(errBuf.String(), "no stacks") {
		t.Errorf("expected a no-stacks message, got: %q", errBuf.String())
	}
}

func TestRunFlamegraphArgValidation(t *testing.T) {
	deps := fgDeps(fgSample, nil, map[string]string{"/s.txt": fgSample}, map[string]string{})
	for _, args := range [][]string{
		{},                         // no pid, no input
		{"notapid"},                // bad pid
		{"1", "2"},                 // too many
		{"--input", "/s.txt", "9"}, // both input and pid
	} {
		var out, errBuf bytes.Buffer
		if code := runFlamegraphWithIO(args, &out, &errBuf, deps); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}
