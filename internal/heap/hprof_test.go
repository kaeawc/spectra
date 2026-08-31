package heap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

// hprofBuilder constructs a spec-valid .hprof byte stream for tests. It doubles
// as executable documentation of the record layout the parser consumes.
type hprofBuilder struct {
	buf    bytes.Buffer
	idSize int
}

func newHPROFBuilder(idSize int) *hprofBuilder {
	b := &hprofBuilder{idSize: idSize}
	b.buf.WriteString("JAVA PROFILE 1.0.2")
	b.buf.WriteByte(0x00)
	b.u4(uint32(idSize))
	b.u8(0) // timestamp
	return b
}

func (b *hprofBuilder) u1(v byte)   { b.buf.WriteByte(v) }
func (b *hprofBuilder) u4(v uint32) { _ = binary.Write(&b.buf, binary.BigEndian, v) }
func (b *hprofBuilder) u8(v uint64) { _ = binary.Write(&b.buf, binary.BigEndian, v) }

// writeIDTo writes an id (idSize bytes, big-endian) into a sub-record body.
func (b *hprofBuilder) writeIDTo(w *bytes.Buffer, v uint64) {
	if b.idSize == 4 {
		_ = binary.Write(w, binary.BigEndian, uint32(v))
		return
	}
	_ = binary.Write(w, binary.BigEndian, v)
}

func (b *hprofBuilder) record(tag byte, body []byte) {
	b.u1(tag)
	b.u4(0) // time
	b.u4(uint32(len(body)))
	b.buf.Write(body)
}

func (b *hprofBuilder) stringRecord(id uint64, s string) {
	var body bytes.Buffer
	b.writeIDTo(&body, id)
	body.WriteString(s)
	b.record(0x01, body.Bytes())
}

func (b *hprofBuilder) loadClass(serial uint32, classObjID uint64, nameID uint64) {
	var body bytes.Buffer
	_ = binary.Write(&body, binary.BigEndian, serial)
	b.writeIDTo(&body, classObjID)
	_ = binary.Write(&body, binary.BigEndian, uint32(0)) // stack serial
	b.writeIDTo(&body, nameID)
	b.record(0x02, body.Bytes())
}

// heap-dump sub-record builders (written into one segment body).

func (b *hprofBuilder) classDump(w *bytes.Buffer, classObjID uint64, instSize uint32) {
	w.WriteByte(0x20)
	b.writeIDTo(w, classObjID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	for i := 0; i < 6; i++ {
		b.writeIDTo(w, 0) // super, loader, signers, protdomain, reserved x2
	}
	_ = binary.Write(w, binary.BigEndian, instSize)
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // const pool
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // static fields
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // instance fields
}

func (b *hprofBuilder) instanceDump(w *bytes.Buffer, objID, classID uint64, nbytes uint32) {
	w.WriteByte(0x21)
	b.writeIDTo(w, objID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	b.writeIDTo(w, classID)
	_ = binary.Write(w, binary.BigEndian, nbytes)
	w.Write(make([]byte, nbytes))
}

func (b *hprofBuilder) objectArrayDump(w *bytes.Buffer, objID uint64, numElems uint32, arrayClassID uint64) {
	w.WriteByte(0x22)
	b.writeIDTo(w, objID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	_ = binary.Write(w, binary.BigEndian, numElems)
	b.writeIDTo(w, arrayClassID)
	w.Write(make([]byte, int(numElems)*b.idSize))
}

func (b *hprofBuilder) primArrayDump(w *bytes.Buffer, objID uint64, numElems uint32, elemType byte, elemSize int) {
	w.WriteByte(0x23)
	b.writeIDTo(w, objID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	_ = binary.Write(w, binary.BigEndian, numElems)
	w.WriteByte(elemType)
	w.Write(make([]byte, int(numElems)*elemSize))
}

func (b *hprofBuilder) rootStickyClass(w *bytes.Buffer, classID uint64) {
	w.WriteByte(0x05)
	b.writeIDTo(w, classID)
}

func (b *hprofBuilder) heapDumpInfo(w *bytes.Buffer, heapID uint32, nameID uint64) {
	w.WriteByte(0xFE)
	_ = binary.Write(w, binary.BigEndian, heapID)
	b.writeIDTo(w, nameID)
}

func (b *hprofBuilder) heapDumpSegment(body []byte) { b.record(0x1C, body) }
func (b *hprofBuilder) heapDumpEnd()                { b.record(0x2C, nil) }

// buildStandardDump builds a dump with 3 A + 2 B instances, one object array of
// A (4 elems), one int primitive array (10 elems), plus a root and a heap-dump
// info record to exercise skip logic. aExtra adds more A instances (for diffs).
func buildStandardDump(idSize int, aExtra int) []byte {
	b := newHPROFBuilder(idSize)
	b.stringRecord(100, "com/acme/A")
	b.stringRecord(101, "com/acme/B")
	b.stringRecord(102, "[Lcom/acme/A;")
	b.loadClass(1, 1, 100)
	b.loadClass(2, 2, 101)
	b.loadClass(3, 3, 102)

	var seg bytes.Buffer
	b.heapDumpInfo(&seg, 1, 100)
	b.rootStickyClass(&seg, 1)
	b.classDump(&seg, 1, 24)
	b.classDump(&seg, 2, 16)
	b.classDump(&seg, 3, 0)
	for i := 0; i < 3+aExtra; i++ {
		b.instanceDump(&seg, uint64(1000+i), 1, 16)
	}
	b.instanceDump(&seg, 2000, 2, 8)
	b.instanceDump(&seg, 2001, 2, 8)
	b.objectArrayDump(&seg, 3000, 4, 3)
	b.primArrayDump(&seg, 4000, 10, 10, 4) // int[], 10 elems, 4 bytes each
	b.heapDumpSegment(seg.Bytes())
	b.heapDumpEnd()
	return b.buf.Bytes()
}

func entryByName(h Histogram, name string) (ClassEntry, bool) {
	for _, e := range h.Entries {
		if e.ClassName == name {
			return e, true
		}
	}
	return ClassEntry{}, false
}

func TestParseHPROFHistogram(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		h, err := ParseHPROF(bytes.NewReader(buildStandardDump(idSize, 0)))
		if err != nil {
			t.Fatalf("idSize %d: ParseHPROF error: %v", idSize, err)
		}
		a, ok := entryByName(h, "com.acme.A")
		if !ok || a.Instances != 3 || a.Bytes != 72 {
			t.Errorf("idSize %d: A = %+v, want instances 3 bytes 72", idSize, a)
		}
		b, ok := entryByName(h, "com.acme.B")
		if !ok || b.Instances != 2 || b.Bytes != 32 {
			t.Errorf("idSize %d: B = %+v, want instances 2 bytes 32", idSize, b)
		}
		oa, ok := entryByName(h, "[Lcom.acme.A;")
		if !ok || oa.Instances != 1 || oa.Bytes != int64(hprofArrayHeaderBytes+4*idSize) {
			t.Errorf("idSize %d: obj array = %+v", idSize, oa)
		}
		pa, ok := entryByName(h, "[I")
		if !ok || pa.Instances != 1 || pa.Bytes != int64(hprofArrayHeaderBytes+10*4) {
			t.Errorf("idSize %d: int array = %+v", idSize, pa)
		}
		wantTotal := int64(72 + 32 + (hprofArrayHeaderBytes + 4*idSize) + (hprofArrayHeaderBytes + 40))
		if h.Total.Bytes != wantTotal || h.Total.Instances != 7 {
			t.Errorf("idSize %d: total = %+v, want bytes %d instances 7", idSize, h.Total, wantTotal)
		}
		// Entries sorted by bytes desc: A is largest.
		if h.Entries[0].ClassName != "com.acme.A" || h.Entries[0].Rank != 1 {
			t.Errorf("idSize %d: top entry = %+v, want com.acme.A rank 1", idSize, h.Entries[0])
		}
	}
}

func TestParseHPROFRejectsBadHeader(t *testing.T) {
	if _, err := ParseHPROF(strings.NewReader("NOT AN HPROF FILE")); err == nil {
		t.Fatal("expected error on bad header")
	}
}

func TestParseHPROFRejectsUnknownSubTag(t *testing.T) {
	b := newHPROFBuilder(8)
	var seg bytes.Buffer
	seg.WriteByte(0x7A) // not a valid heap-dump sub-record tag
	b.heapDumpSegment(seg.Bytes())
	if _, err := ParseHPROF(bytes.NewReader(b.buf.Bytes())); err == nil {
		t.Fatal("expected error on unknown sub-record tag")
	}
}

func TestParseHPROFRejectsTruncatedSegment(t *testing.T) {
	b := newHPROFBuilder(8)
	// Emit a heap-dump segment header claiming more bytes than we provide, then
	// stop the stream mid-segment (after one valid sub-record).
	b.u1(0x1C)
	b.u4(0)  // time
	b.u4(64) // declared length, larger than the body below
	var seg bytes.Buffer
	b.rootStickyClass(&seg, 1) // 1 + 8 bytes, well under 64
	b.buf.Write(seg.Bytes())
	if _, err := ParseHPROF(bytes.NewReader(b.buf.Bytes())); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected io.ErrUnexpectedEOF on truncated segment, got %v", err)
	}
}

func TestParseHPROFRejectsOversizedStringRecord(t *testing.T) {
	b := newHPROFBuilder(8)
	// A STRING record header claiming a body far beyond the allocation cap must
	// be rejected before allocating (guards against corrupt/hostile lengths).
	b.u1(0x01)
	b.u4(0)                                 // time
	b.u4(uint32(maxHPROFStringBytes) + 100) // declared record length
	b.writeIDTo(&b.buf, 1)                  // string id; body would follow but never does
	if _, err := ParseHPROF(bytes.NewReader(b.buf.Bytes())); err == nil {
		t.Fatal("expected error on oversized string record")
	}
}

func TestParseHPROFComposesWithAnalyzer(t *testing.T) {
	before, err := ParseHPROF(bytes.NewReader(buildStandardDump(8, 0)))
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseHPROF(bytes.NewReader(buildStandardDump(8, 100))) // 100 extra A instances
	if err != nil {
		t.Fatal(err)
	}
	suspects, err := DefaultAnalyzer{}.RankGrowth(before, after, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(suspects) == 0 || suspects[0].Name != "com.acme.A" {
		t.Fatalf("top growth suspect = %+v, want com.acme.A", suspects)
	}
	if suspects[0].DeltaCount != 100 {
		t.Errorf("A delta count = %d, want 100", suspects[0].DeltaCount)
	}
}

func TestHPROFParserImplementsParser(t *testing.T) {
	var _ Parser = HPROFParser{}
	snap, err := HPROFParser{}.ParseSnapshot(buildStandardDump(8, 0))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Runtime() != RuntimeJVM || len(snap.Records()) == 0 {
		t.Fatalf("snapshot = %+v", snap)
	}
}
