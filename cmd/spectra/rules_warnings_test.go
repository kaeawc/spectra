package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintSnapshotWarnings(t *testing.T) {
	var buf bytes.Buffer
	printSnapshotWarnings(&buf, []string{"jvm: jps not found in PATH"})
	if !strings.Contains(buf.String(), "warning: jvm: jps not found in PATH") {
		t.Fatalf("render = %q", buf.String())
	}

	buf.Reset()
	printSnapshotWarnings(&buf, nil)
	if buf.Len() != 0 {
		t.Fatalf("no warnings should print nothing, got %q", buf.String())
	}
}
