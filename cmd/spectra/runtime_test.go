package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/runtimeattach"
)

func fixedLookup(p runtimeattach.Process, ok bool) procLookup {
	return func(int) (runtimeattach.Process, bool) { return p, ok }
}

func TestRunRuntimeClassifiesJVM(t *testing.T) {
	lookup := fixedLookup(runtimeattach.Process{PID: 321, Command: "java", ExecutablePath: "/usr/bin/java", CommandLine: "java -jar app.jar"}, true)
	var out, errBuf bytes.Buffer
	if code := runRuntimeWithIO([]string{"321"}, &out, &errBuf, lookup, runtimeattach.DefaultProbes()); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	s := out.String()
	if !strings.Contains(s, "runtime:  jvm") || !strings.Contains(s, "spectra jvm 321") {
		t.Errorf("expected jvm classification routed to `spectra jvm`, got:\n%s", s)
	}
}

func TestRunRuntimeAcceptsAttachVerb(t *testing.T) {
	lookup := fixedLookup(runtimeattach.Process{PID: 9, Command: "node", ExecutablePath: "/usr/local/bin/node"}, true)
	var out, errBuf bytes.Buffer
	if code := runRuntimeWithIO([]string{"attach", "9"}, &out, &errBuf, lookup, runtimeattach.DefaultProbes()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "runtime:  node") {
		t.Errorf("expected node classification, got:\n%s", out.String())
	}
}

func TestRunRuntimeMissingProcess(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := runRuntimeWithIO([]string{"404"}, &out, &errBuf, fixedLookup(runtimeattach.Process{}, false), runtimeattach.DefaultProbes()); code != 1 {
		t.Fatalf("exit = %d, want 1 for missing process", code)
	}
}

func TestRunRuntimeInvalidArgs(t *testing.T) {
	lookup := fixedLookup(runtimeattach.Process{}, true)
	cases := [][]string{{}, {"notapid"}, {"0"}, {"1", "2"}}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := runRuntimeWithIO(args, &out, &errBuf, lookup, runtimeattach.DefaultProbes()); code != 2 {
			t.Errorf("args %v: exit = %d, want 2", args, code)
		}
	}
}

func TestRunRuntimeJSON(t *testing.T) {
	lookup := fixedLookup(runtimeattach.Process{PID: 5, Command: "server", ExecutablePath: "/opt/server"}, true)
	var out, errBuf bytes.Buffer
	if code := runRuntimeWithIO([]string{"--json", "5"}, &out, &errBuf, lookup, runtimeattach.DefaultProbes()); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), `"runtime"`) || !strings.Contains(out.String(), `"capabilities"`) {
		t.Errorf("expected JSON with runtime + capabilities, got:\n%s", out.String())
	}
}
