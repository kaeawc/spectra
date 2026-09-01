package heap

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// classDumpFields writes a CLASS_DUMP with an explicit superclass and instance
// field type list (field name string ids are irrelevant to the graph).
func (b *hprofBuilder) classDumpFields(w *bytes.Buffer, classObjID, superID uint64, instSize uint32, fieldTypes []byte) {
	w.WriteByte(0x20)
	b.writeIDTo(w, classObjID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	b.writeIDTo(w, superID)
	for i := 0; i < 5; i++ {
		b.writeIDTo(w, 0)
	}
	_ = binary.Write(w, binary.BigEndian, instSize)
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // const pool
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // static fields
	_ = binary.Write(w, binary.BigEndian, uint16(len(fieldTypes)))
	for _, ft := range fieldTypes {
		b.writeIDTo(w, 0) // field name string id
		w.WriteByte(ft)
	}
}

func (b *hprofBuilder) instanceDumpData(w *bytes.Buffer, objID, classID uint64, data []byte) {
	w.WriteByte(0x21)
	b.writeIDTo(w, objID)
	_ = binary.Write(w, binary.BigEndian, uint32(0))
	b.writeIDTo(w, classID)
	_ = binary.Write(w, binary.BigEndian, uint32(len(data)))
	w.Write(data)
}

func (b *hprofBuilder) rootJNIGlobal(w *bytes.Buffer, objID uint64) {
	w.WriteByte(0x01)
	b.writeIDTo(w, objID)
	b.writeIDTo(w, 0) // jni global ref id
}

// idBytes encodes an object id in the builder's id size.
func (b *hprofBuilder) idBytes(v uint64) []byte {
	var w bytes.Buffer
	b.writeIDTo(&w, v)
	return w.Bytes()
}

func buildGraphDump(idSize int) []byte {
	b := newHPROFBuilder(idSize)
	b.stringRecord(100, "com/acme/A")
	b.stringRecord(101, "com/acme/B")
	b.stringRecord(102, "[Lcom/acme/A;")
	b.loadClass(1, 1, 100)
	b.loadClass(2, 2, 101)
	b.loadClass(3, 3, 102)

	var seg bytes.Buffer
	// A: super=0, one object field ("next").
	b.classDumpFields(&seg, 1, 0, uint32(idSize), []byte{hprofTypeObject})
	// B: super=A, one int field of its own (so B's layout is [int] then A's [object]).
	b.classDumpFields(&seg, 2, 1, uint32(idSize+4), []byte{hprofTypeInt})
	// Object array class (no fields).
	b.classDumpFields(&seg, 3, 0, 0, nil)

	// a(1000) class A: object field -> b(1001).
	b.instanceDumpData(&seg, 1000, 1, b.idBytes(1001))
	// b(1001) class B: [int=7][inherited A object -> a(1000)].
	bData := append([]byte{0, 0, 0, 7}, b.idBytes(1000)...)
	b.instanceDumpData(&seg, 1001, 2, bData)
	// object array 2000 of A, elements [1000, null].
	var arr bytes.Buffer
	arr.WriteByte(0x22)
	b.writeIDTo(&arr, 2000)
	_ = binary.Write(&arr, binary.BigEndian, uint32(0))
	_ = binary.Write(&arr, binary.BigEndian, uint32(2))
	b.writeIDTo(&arr, 3)
	arr.Write(b.idBytes(1000))
	arr.Write(b.idBytes(0))
	seg.Write(arr.Bytes())
	// int primitive array 2001 (3 elems).
	b.primArrayDump(&seg, 2001, 3, hprofTypeInt, 4)
	// GC root -> a(1000).
	b.rootJNIGlobal(&seg, 1000)

	b.heapDumpSegment(seg.Bytes())
	b.heapDumpEnd()
	return b.finish()
}

func (b *hprofBuilder) finish() []byte { return b.buf.Bytes() }

func TestParseObjectGraph(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		g, err := ParseObjectGraph(bytes.NewReader(buildGraphDump(idSize)))
		if err != nil {
			t.Fatalf("idSize %d: ParseObjectGraph: %v", idSize, err)
		}
		if g.Unresolved != 0 {
			t.Errorf("idSize %d: unresolved = %d, want 0", idSize, g.Unresolved)
		}
		a := g.Nodes[1000]
		if a == nil {
			t.Fatalf("idSize %d: node a missing", idSize)
		}
		if a.ClassName != "com/acme/A" || a.ShallowSize != int64(idSize) {
			t.Errorf("idSize %d: a = %+v", idSize, a)
		}
		if len(a.Out) != 1 || a.Out[0] != 1001 {
			t.Errorf("idSize %d: a.Out = %v, want [1001]", idSize, a.Out)
		}
		// b's int field must be skipped and its inherited A.object field read → a.
		b := g.Nodes[1001]
		if b == nil || len(b.Out) != 1 || b.Out[0] != 1000 {
			t.Errorf("idSize %d: b.Out = %v, want [1000] (superclass field via correct offset)", idSize, outOf(b))
		}
		// object array: null element dropped.
		arr := g.Nodes[2000]
		if arr == nil || len(arr.Out) != 1 || arr.Out[0] != 1000 {
			t.Errorf("idSize %d: array out = %v, want [1000]", idSize, outOf(arr))
		}
		if arr.ClassName != "[Lcom/acme/A;" {
			t.Errorf("idSize %d: array class = %q", idSize, arr.ClassName)
		}
		// primitive array: present, no edges.
		if p := g.Nodes[2001]; p == nil || len(p.Out) != 0 {
			t.Errorf("idSize %d: prim array = %+v", idSize, p)
		}
		// root.
		if len(g.Roots) != 1 || g.Roots[0] != 1000 {
			t.Errorf("idSize %d: roots = %v, want [1000]", idSize, g.Roots)
		}
	}
}

func outOf(n *ObjectNode) []uint64 {
	if n == nil {
		return nil
	}
	return n.Out
}

func TestParseObjectGraphRejectsBadHeader(t *testing.T) {
	if _, err := ParseObjectGraph(bytes.NewReader([]byte("not an hprof"))); err == nil {
		t.Error("expected an error on a bad header")
	}
}
