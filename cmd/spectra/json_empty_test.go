package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
)

// Agents pipe these straight into jq; "no results" must still be an array.

func TestJVMListJSONEmptyIsArray(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := emitJVMList(&out, &errBuf, nil, true); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
	if !strings.Contains(errBuf.String(), "no running JVMs") {
		t.Errorf("stderr = %q, want the hint", errBuf.String())
	}
}

func TestJVMListTextEmptyPrintsNothing(t *testing.T) {
	var out, errBuf bytes.Buffer
	emitJVMList(&out, &errBuf, []jvm.Info{}, false)
	if out.Len() != 0 {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestFindingsJSONEmptyIsArray(t *testing.T) {
	var out bytes.Buffer
	writeFindingsJSON(&out, nil)
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Errorf("stdout = %q, want []", got)
	}
}
