package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/heap"
)

// writeHPROF writes a minimal but spec-valid 8-byte-id .hprof containing
// `count` instances of com.acme.Widget (24 bytes each) and returns its path.
func writeHPROF(t *testing.T, name string, count int) string {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("JAVA PROFILE 1.0.2")
	buf.WriteByte(0)
	be := func(v any) { _ = binary.Write(&buf, binary.BigEndian, v) }
	be(uint32(8)) // id size
	be(uint64(0)) // timestamp

	record := func(tag byte, body []byte) {
		buf.WriteByte(tag)
		be(uint32(0))
		be(uint32(len(body)))
		buf.Write(body)
	}

	// STRING id=1 "com/acme/Widget"
	var s bytes.Buffer
	_ = binary.Write(&s, binary.BigEndian, uint64(1))
	s.WriteString("com/acme/Widget")
	record(0x01, s.Bytes())

	// LOAD_CLASS serial=1 classId=7 nameId=1
	var lc bytes.Buffer
	_ = binary.Write(&lc, binary.BigEndian, uint32(1))
	_ = binary.Write(&lc, binary.BigEndian, uint64(7))
	_ = binary.Write(&lc, binary.BigEndian, uint32(0))
	_ = binary.Write(&lc, binary.BigEndian, uint64(1))
	record(0x02, lc.Bytes())

	// HEAP DUMP SEGMENT: CLASS_DUMP(classId=7, instSize=24) + count instances.
	var seg bytes.Buffer
	seg.WriteByte(0x20)
	_ = binary.Write(&seg, binary.BigEndian, uint64(7)) // class obj id
	_ = binary.Write(&seg, binary.BigEndian, uint32(0)) // stack serial
	for i := 0; i < 6; i++ {
		_ = binary.Write(&seg, binary.BigEndian, uint64(0)) // super/loader/... reserved
	}
	_ = binary.Write(&seg, binary.BigEndian, uint32(24)) // instance size
	_ = binary.Write(&seg, binary.BigEndian, uint16(0))  // const pool
	_ = binary.Write(&seg, binary.BigEndian, uint16(0))  // static fields
	_ = binary.Write(&seg, binary.BigEndian, uint16(0))  // instance fields
	for i := 0; i < count; i++ {
		seg.WriteByte(0x21)
		_ = binary.Write(&seg, binary.BigEndian, uint64(1000+i)) // obj id
		_ = binary.Write(&seg, binary.BigEndian, uint32(0))      // stack serial
		_ = binary.Write(&seg, binary.BigEndian, uint64(7))      // class id
		_ = binary.Write(&seg, binary.BigEndian, uint32(0))      // nbytes following
	}
	record(0x1C, seg.Bytes())
	record(0x2C, nil) // heap dump end

	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

func TestReadHistogramFileAutoDetectsHPROF(t *testing.T) {
	hist, err := readHistogramFile(writeHPROF(t, "dump.hprof", 5))
	if err != nil {
		t.Fatalf("readHistogramFile: %v", err)
	}
	var widget heap.ClassEntry
	for _, e := range hist.Entries {
		if e.ClassName == "com.acme.Widget" {
			widget = e
		}
	}
	if widget.Instances != 5 || widget.Bytes != 120 {
		t.Fatalf("Widget = %+v, want 5 instances / 120 bytes", widget)
	}
}

func TestHeapHPROFCompareRanksGrowth(t *testing.T) {
	before, err := heap.ParseHPROFFile(writeHPROF(t, "before.hprof", 5))
	if err != nil {
		t.Fatal(err)
	}
	after, err := heap.ParseHPROFFile(writeHPROF(t, "after.hprof", 105))
	if err != nil {
		t.Fatal(err)
	}
	growth := heap.RankGrowthSuspects(before, after, 10)
	if len(growth) == 0 || growth[0].ClassName != "com.acme.Widget" {
		t.Fatalf("top growth suspect = %+v, want com.acme.Widget", growth)
	}
	if growth[0].DeltaCount != 100 {
		t.Fatalf("delta count = %d, want 100", growth[0].DeltaCount)
	}

	var buf bytes.Buffer
	printHeapSuspectsFor(&buf, "after.hprof", heap.RankHistogramSuspects(after, 5), after.Total)
	if !strings.Contains(buf.String(), "after.hprof") || !strings.Contains(buf.String(), "largest classes") {
		t.Fatalf("render missing header:\n%s", buf.String())
	}
}
