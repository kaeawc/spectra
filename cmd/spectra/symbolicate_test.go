package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/symbolicate"
)

func fixedSymRunner(out string) symbolicate.Runner {
	return func(context.Context, string, ...string) ([]byte, error) { return []byte(out), nil }
}

func TestRunSymbolicatePositional(t *testing.T) {
	runner := fixedSymRunner("main (in App) (main.c:10)\n")
	var out, errBuf bytes.Buffer
	code := runSymbolicateWithIO([]string{"-o", "/App", "-l", "0x1000", "0x1010"}, strings.NewReader(""), &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "main") || !strings.Contains(out.String(), "main.c:10") {
		t.Errorf("expected symbolicated frame, got:\n%s", out.String())
	}
}

func TestRunSymbolicateStdin(t *testing.T) {
	runner := fixedSymRunner("foo (in App)\n")
	var out, errBuf bytes.Buffer
	code := runSymbolicateWithIO([]string{"-o", "/App", "-l", "0x1000"}, strings.NewReader("0x1010\n"), &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "foo") {
		t.Errorf("expected frame from stdin address, got:\n%s", out.String())
	}
}

func TestRunSymbolicateRequiresFlags(t *testing.T) {
	runner := fixedSymRunner("")
	cases := [][]string{
		{"0x1010"},                     // no -o/-l
		{"-o", "/App", "0x1010"},       // no -l
		{"-o", "/App", "-l", "0x1000"}, // no addresses (empty stdin)
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runSymbolicateWithIO(args, strings.NewReader(""), &out, &errBuf, runner); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunSymbolicateJSON(t *testing.T) {
	runner := fixedSymRunner("main (in App) (main.c:10)\n")
	var out, errBuf bytes.Buffer
	code := runSymbolicateWithIO([]string{"--json", "-o", "/App", "-l", "0x1000", "0x1010"}, strings.NewReader(""), &out, &errBuf, runner)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"frames"`) || !strings.Contains(out.String(), `"resolved"`) {
		t.Errorf("expected JSON, got:\n%s", out.String())
	}
}
