package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/crashquery"
)

func memResourceIPS(app, when string) string {
	return `{"app_name":"` + app + `","timestamp":"` + when + `","bug_type":"309","name":"` + app + `"}
{"procName":"` + app + `","pid":1,"exception":{"type":"EXC_RESOURCE","subtype":"MEMORY (Limit 1500 MB Observed 1620 MB)"},"termination":{"namespace":"RESOURCE","indicator":"MEMORY"},"faultingThread":0,"threads":[{"triggered":true,"frames":[]}],"usedImages":[]}`
}

func TestRunCrashOOMRenders(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"mem.ips", "seg.ips"}, nil },
		Read: func(f string) ([]byte, error) {
			if f == "mem.ips" {
				return []byte(memResourceIPS("Hungry", "2026-08-05 10:00:00.00 -0500")), nil
			}
			return []byte(ipsReport("Fine", "2026-08-05 09:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	if code := runCrashOOMWithDeps(false, 25, &out, &errBuf, deps); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "Hungry") || strings.Contains(s, "Fine") {
		t.Errorf("expected only the memory kill (Hungry, not Fine); got:\n%s", s)
	}
	if !strings.Contains(s, "limit") {
		t.Errorf("expected the limit note; got:\n%s", s)
	}
}

func TestRunCrashOOMNone(t *testing.T) {
	deps := crashquery.Source{
		List: func(context.Context) ([]string, error) { return []string{"seg.ips"}, nil },
		Read: func(string) ([]byte, error) {
			return []byte(ipsReport("Fine", "2026-08-05 09:00:00.00 -0500", "EXC_BAD_ACCESS")), nil
		},
	}
	var out, errBuf bytes.Buffer
	runCrashOOMWithDeps(false, 25, &out, &errBuf, deps)
	if !strings.Contains(out.String(), "No memory/OOM kills") {
		t.Errorf("out = %q", out.String())
	}
}

func TestRunCrashOOMRejectsPositional(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runCrashOOM([]string{"extra"}, &out, &errBuf); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
