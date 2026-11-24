package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashquery"
)

func TestRunCrashSignaturesRenders(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"a.ips", "b.ips"}, nil },
		Read: func(f string) ([]byte, error) {
			when := "2026-08-05 10:00:00.00 -0500"
			if f == "a.ips" {
				when = "2026-08-01 10:00:00.00 -0500"
			}
			return []byte(ipsReport("MyApp", when, "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashSignaturesWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "[2×] MyApp") {
		t.Errorf("expected a 2x MyApp signature; got:\n%s", s)
	}
}

func TestRunCrashSignaturesEmpty(t *testing.T) {
	deps := crashquery.Source{List: func(context.Context) ([]string, error) { return nil, nil }, Read: func(string) ([]byte, error) { return nil, nil }}
	var out, errBuf bytes.Buffer
	runCrashSignaturesWithDeps(false, 25, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "No crash reports found") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunCrashSignaturesRejectsPositional(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashSignatures([]string{"extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
