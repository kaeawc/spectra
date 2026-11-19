package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDU(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "big.bin"), make([]byte, 256*1024), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := runDUContext(context.Background(), []string{"--top", "3", root}, &out, &errBuf); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errBuf.String())
	}
	if s := out.String(); !strings.Contains(s, "big.bin") || !strings.Contains(s, "Top directories") {
		t.Errorf("unexpected output:\n%s", s)
	}

	out.Reset()
	if code := runDUContext(context.Background(), []string{"--json", "--min-size", "100K", root}, &out, &errBuf); code != 0 {
		t.Fatalf("json exit = %d", code)
	}
	if !strings.Contains(out.String(), `"top_files"`) || !strings.Contains(out.String(), `"partial": false`) {
		t.Errorf("unexpected JSON:\n%s", out.String())
	}

	errBuf.Reset()
	if code := runDUContext(context.Background(), []string{filepath.Join(root, "nope")}, &out, &errBuf); code != 1 {
		t.Errorf("missing path exit = %d, want 1", code)
	}
	if code := runDUContext(context.Background(), []string{}, &out, &errBuf); code != 2 {
		t.Errorf("no args exit = %d, want 2", code)
	}
}
