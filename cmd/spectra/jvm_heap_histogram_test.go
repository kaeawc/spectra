package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/heap"
)

const histBefore = ` num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:            10            1000  [B (java.base@21)
   2:             5             200  java.lang.String (java.base@21)
Total           15            1200
`

const histAfter = ` num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:           100           50000  [B (java.base@21)
   2:             5             200  java.lang.String (java.base@21)
Total          105           50200
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestHeapHistogramCompareRanksGrowth(t *testing.T) {
	before, err := readHistogramFile(writeTemp(t, "before.txt", histBefore))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	after, err := readHistogramFile(writeTemp(t, "after.txt", histAfter))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}

	growth := heap.RankGrowthSuspects(before, after, 10)
	if len(growth) == 0 {
		t.Fatalf("expected at least one growth suspect")
	}
	// The [B (byte array) class grew the most; it should rank first.
	if !strings.Contains(growth[0].ClassName, "[B") {
		t.Fatalf("top growth suspect = %q, want the byte-array class", growth[0].ClassName)
	}
	if growth[0].DeltaBytes != 49000 {
		t.Fatalf("top growth delta bytes = %d, want 49000", growth[0].DeltaBytes)
	}

	var buf bytes.Buffer
	printGrowthSuspects(&buf, growth)
	if !strings.Contains(buf.String(), "growth suspects") {
		t.Fatalf("render missing header:\n%s", buf.String())
	}
}

func TestHeapSuspectsRender(t *testing.T) {
	hist, err := heap.ParseHistogram(histAfter)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ranked := heap.RankHistogramSuspects(hist, 5)
	var buf bytes.Buffer
	printHeapSuspects(&buf, 4012, ranked, hist.Total)
	out := buf.String()
	if !strings.Contains(out, "Heap histogram for PID 4012") {
		t.Fatalf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "largest classes") {
		t.Fatalf("missing suspects section:\n%s", out)
	}
}
