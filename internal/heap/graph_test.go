package heap

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// classDumpFields writes a CLASS_DUMP with an explicit superclass, object-typed
// static fields, and instance field type list (field name ids are irrelevant
// to the graph).
func (b *hprofBuilder) classDumpFields(w *bytes.Buffer, classObjID, superID uint64, instSize uint32, statics []uint64, fieldTypes []byte) {
	w.WriteByte(0x20)
	b.writeIDTo(w, classObjID)
	_ = binary.Write(w, binary.BigEndian, uint32(0)) // stack serial
	b.writeIDTo(w, superID)
	for i := 0; i < 5; i++ {
		b.writeIDTo(w, 0)
	}
	_ = binary.Write(w, binary.BigEndian, instSize)
	_ = binary.Write(w, binary.BigEndian, uint16(0)) // const pool
	// Static fields: an int (skipped) then each object reference.
	_ = binary.Write(w, binary.BigEndian, uint16(len(statics)+1))
	b.writeIDTo(w, 0)
	w.WriteByte(hprofTypeInt)
	_ = binary.Write(w, binary.BigEndian, uint32(42))
	for _, ref := range statics {
		b.writeIDTo(w, 0)
		w.WriteByte(hprofTypeObject)
		b.writeIDTo(w, ref)
	}
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

func (b *hprofBuilder) objectArrayElems(w *bytes.Buffer, objID, arrayClassID uint64, elems ...uint64) {
	w.WriteByte(0x22)
	b.writeIDTo(w, objID)
	_ = binary.Write(w, binary.BigEndian, uint32(0))
	_ = binary.Write(w, binary.BigEndian, uint32(len(elems)))
	b.writeIDTo(w, arrayClassID)
	for _, e := range elems {
		b.writeIDTo(w, e)
	}
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

func (b *hprofBuilder) finish() []byte { return b.buf.Bytes() }

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
	b.classDumpFields(&seg, 1, 0, uint32(idSize), nil, []byte{hprofTypeObject})
	// B: super=A, one int field of its own (so B's layout is [int] then A's [object]).
	b.classDumpFields(&seg, 2, 1, uint32(idSize+4), nil, []byte{hprofTypeInt})
	// Object array class (no fields).
	b.classDumpFields(&seg, 3, 0, 0, nil, nil)

	// a(1000) class A: object field -> b(1001).
	b.instanceDumpData(&seg, 1000, 1, b.idBytes(1001))
	// b(1001) class B: [int=7][inherited A object -> a(1000)].
	bData := append([]byte{0, 0, 0, 7}, b.idBytes(1000)...)
	b.instanceDumpData(&seg, 1001, 2, bData)
	// object array 2000 of A, elements [1000, null].
	b.objectArrayElems(&seg, 2000, 3, 1000, 0)
	// int primitive array 2001 (3 elems).
	b.primArrayDump(&seg, 2001, 3, hprofTypeInt, 4)
	// GC root -> a(1000).
	b.rootJNIGlobal(&seg, 1000)

	b.heapDumpSegment(seg.Bytes())
	b.heapDumpEnd()
	return b.finish()
}

// outIDs returns the object ids object id references, or nil if absent.
func outIDs(g *ObjectGraph, id uint64) []uint64 {
	i, ok := g.Index(id)
	if !ok {
		return nil
	}
	var ids []uint64
	for _, o := range g.Out(i) {
		ids = append(ids, g.ID(o))
	}
	return ids
}

func equalIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseObjectGraph(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		g, err := ParseObjectGraph(bytes.NewReader(buildGraphDump(idSize)))
		if err != nil {
			t.Fatalf("idSize %d: ParseObjectGraph: %v", idSize, err)
		}
		if g.Unresolved != 0 {
			t.Errorf("idSize %d: unresolved = %d, want 0", idSize, g.Unresolved)
		}
		a, ok := g.Index(1000)
		if !ok {
			t.Fatalf("idSize %d: node a missing", idSize)
		}
		if g.ClassName(a) != "com.acme.A" || g.ShallowSize(a) != int64(idSize) {
			t.Errorf("idSize %d: a = %s/%d", idSize, g.ClassName(a), g.ShallowSize(a))
		}
		// Each instance references its class object first, then its fields.
		if got := outIDs(g, 1000); !equalIDs(got, []uint64{1, 1001}) {
			t.Errorf("idSize %d: a.Out = %v, want [1 1001]", idSize, got)
		}
		// b's int field must be skipped and its inherited A.object field read → a.
		if got := outIDs(g, 1001); !equalIDs(got, []uint64{2, 1000}) {
			t.Errorf("idSize %d: b.Out = %v, want [2 1000] (superclass field via correct offset)", idSize, got)
		}
		// object array: null element dropped.
		if got := outIDs(g, 2000); !equalIDs(got, []uint64{1000}) {
			t.Errorf("idSize %d: array out = %v, want [1000]", idSize, got)
		}
		if arr, _ := g.Index(2000); g.ClassName(arr) != "[Lcom.acme.A;" {
			t.Errorf("idSize %d: array class = %q", idSize, g.ClassName(arr))
		}
		// primitive array: present, no edges.
		if p, ok := g.Index(2001); !ok || len(g.Out(p)) != 0 || g.ClassName(p) != "[I" {
			t.Errorf("idSize %d: prim array missing or has edges", idSize)
		}
		// class B is a node referencing its superclass A.
		if cb, ok := g.Index(2); !ok || g.DisplayName(cb) != "class com.acme.B" || !equalIDs(outIDs(g, 2), []uint64{1}) {
			t.Errorf("idSize %d: class object B = %v", idSize, outIDs(g, 2))
		}
		if roots := g.Roots(); len(roots) != 1 || g.ID(roots[0]) != 1000 {
			t.Errorf("idSize %d: roots = %v, want [1000]", idSize, roots)
		}
	}
}

func TestParseObjectGraphInstanceBeforeClassDump(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		b := newHPROFBuilder(idSize)
		b.stringRecord(100, "com/acme/A")
		b.loadClass(1, 1, 100)
		var seg bytes.Buffer
		b.rootJNIGlobal(&seg, 10)
		b.instanceDumpData(&seg, 10, 1, b.idBytes(11))
		b.instanceDumpData(&seg, 11, 1, b.idBytes(0))
		b.classDumpFields(&seg, 1, 0, 24, nil, []byte{hprofTypeObject})
		b.heapDumpSegment(seg.Bytes())
		g, err := ParseObjectGraph(bytes.NewReader(b.finish()))
		if err != nil {
			t.Fatal(err)
		}
		if got := outIDs(g, 10); !equalIDs(got, []uint64{1, 11}) {
			t.Errorf("idSize %d: deferred instance out = %v, want [1 11]", idSize, got)
		}
		if i, _ := g.Index(10); g.ShallowSize(i) != 24 {
			t.Errorf("idSize %d: deferred shallow = %d, want 24", idSize, g.ShallowSize(i))
		}
	}
}

func TestParseObjectGraphRejectsBadHeader(t *testing.T) {
	if _, err := ParseObjectGraph(bytes.NewReader([]byte("not an hprof"))); err == nil {
		t.Error("expected an error on a bad header")
	}
}

// synthDump builds HPROF streams for retained-size scenarios. Every class has
// the layout [int, object, object] and is sticky-rooted like a system class;
// instance shallow size is the class's declared instance size.
type synthDump struct {
	b       *hprofBuilder
	seg     bytes.Buffer
	nextStr uint64
}

func newSynthDump(idSize int) *synthDump {
	return &synthDump{b: newHPROFBuilder(idSize), nextStr: 500}
}

func (s *synthDump) class(id uint64, name string, instSize uint32, statics ...uint64) {
	s.nextStr++
	s.b.stringRecord(s.nextStr, name)
	s.b.loadClass(uint32(id), id, s.nextStr)
	s.b.classDumpFields(&s.seg, id, 0, instSize, statics, []byte{hprofTypeInt, hprofTypeObject, hprofTypeObject})
	s.b.rootStickyClass(&s.seg, id)
}

func (s *synthDump) obj(id, class uint64, refs ...uint64) {
	data := []byte{0, 0, 0, 7}
	for i := 0; i < 2; i++ {
		var ref uint64
		if i < len(refs) {
			ref = refs[i]
		}
		data = append(data, s.b.idBytes(ref)...)
	}
	s.b.instanceDumpData(&s.seg, id, class, data)
}

func (s *synthDump) root(id uint64) { s.b.rootJNIGlobal(&s.seg, id) }

func (s *synthDump) parse(t *testing.T) *RetainedSnapshot {
	t.Helper()
	s.b.heapDumpSegment(s.seg.Bytes())
	s.b.heapDumpEnd()
	g, err := ParseObjectGraph(bytes.NewReader(s.b.finish()))
	if err != nil {
		t.Fatalf("ParseObjectGraph: %v", err)
	}
	return ComputeRetained(g)
}

// Shallow sizes are powers of two so every retained sum is unambiguous.
const (
	clsA = 1
	clsB = 2
	clsC = 3
	clsD = 4
	clsE = 5
)

func standardClasses(s *synthDump) {
	s.class(clsA, "A", 1)
	s.class(clsB, "B", 2)
	s.class(clsC, "C", 4)
	s.class(clsD, "D", 8)
	s.class(clsE, "E", 16)
}

func wantRetained(t *testing.T, s *RetainedSnapshot, want map[uint64]int64) {
	t.Helper()
	for id, w := range want {
		i, ok := s.Graph.Index(id)
		if !ok {
			t.Errorf("object 0x%x missing", id)
			continue
		}
		if got := s.Retained(i); got != w {
			t.Errorf("retained(0x%x) = %d, want %d", id, got, w)
		}
	}
}

func TestRetainedFromHPROFScenarios(t *testing.T) {
	scenarios := []struct {
		name  string
		build func(s *synthDump)
		want  map[uint64]int64
	}{
		{
			name: "chain",
			build: func(s *synthDump) {
				s.obj(10, clsA, 11)
				s.obj(11, clsB, 12)
				s.obj(12, clsC)
				s.root(10)
			},
			want: map[uint64]int64{10: 7, 11: 6, 12: 4},
		},
		{
			name: "diamond",
			build: func(s *synthDump) {
				s.obj(10, clsA, 11, 12)
				s.obj(11, clsB, 13)
				s.obj(12, clsC, 13)
				s.obj(13, clsD)
				s.root(10)
			},
			// d is shared by b and c, so neither retains it; a does.
			want: map[uint64]int64{10: 15, 11: 2, 12: 4, 13: 8},
		},
		{
			name: "shared across roots",
			build: func(s *synthDump) {
				s.obj(10, clsA, 13)
				s.obj(11, clsB, 13)
				s.obj(13, clsD)
				s.root(10)
				s.root(11)
			},
			want: map[uint64]int64{10: 1, 11: 2, 13: 8},
		},
		{
			name: "cycle",
			build: func(s *synthDump) {
				s.obj(10, clsA, 11)
				s.obj(11, clsB, 12)
				s.obj(12, clsC, 10)
				s.root(10)
			},
			want: map[uint64]int64{10: 7, 11: 6, 12: 4},
		},
		{
			name: "unreachable",
			build: func(s *synthDump) {
				s.obj(10, clsA)
				s.obj(20, clsD, 21) // orphan subgraph
				s.obj(21, clsE, 20)
				s.root(10)
			},
			want: map[uint64]int64{10: 1, 20: 0, 21: 0},
		},
	}
	for _, idSize := range []int{8, 4} {
		for _, sc := range scenarios {
			t.Run(sc.name, func(t *testing.T) {
				s := newSynthDump(idSize)
				standardClasses(s)
				sc.build(s)
				snap := s.parse(t)
				wantRetained(t, snap, sc.want)
				if snap.ReachableObjects < 5 {
					t.Errorf("idSize %d: reachable = %d, want classes counted", idSize, snap.ReachableObjects)
				}
			})
		}
	}
}

func TestRetainedFromHPROFArrays(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		s := newSynthDump(idSize)
		standardClasses(s)
		s.class(9, "[LA;", 0)
		s.b.objectArrayElems(&s.seg, 30, 9, 10, 11, 0)
		s.obj(10, clsA, 31)
		s.obj(11, clsB)
		s.b.primArrayDump(&s.seg, 31, 5, hprofTypeInt, 4)
		s.root(30)
		snap := s.parse(t)

		arr := int64(hprofArrayHeaderBytes + 3*idSize)
		prim := int64(hprofArrayHeaderBytes + 5*4)
		wantRetained(t, snap, map[uint64]int64{30: arr + 1 + prim + 2, 10: 1 + prim, 11: 2, 31: prim})
		if snap.TotalShallowBytes != arr+prim+1+2 {
			t.Errorf("idSize %d: total = %d", idSize, snap.TotalShallowBytes)
		}
	}
}

func TestRetainedFromHPROFStaticField(t *testing.T) {
	// The class object of S holds a large object through a static field, so the
	// class (not any instance) retains it.
	for _, idSize := range []int{8, 4} {
		s := newSynthDump(idSize)
		standardClasses(s)
		s.class(7, "com/acme/Cache", 0, 40)
		s.obj(40, clsE, 41)
		s.obj(41, clsD)
		snap := s.parse(t)
		wantRetained(t, snap, map[uint64]int64{7: 24, 40: 24})

		res := RankRetained(snap, 1)
		if len(res.Suspects) != 1 || res.Suspects[0].ClassName != "class com.acme.Cache" {
			t.Errorf("idSize %d: top object = %+v, want class com.acme.Cache", idSize, res.Suspects)
		}
		for _, c := range RankRetained(snap, 0).Classes {
			if c.ClassName == javaLangClass && (c.RetainedBytes != 24 || c.Instances != 6) {
				t.Errorf("idSize %d: java.lang.Class = %+v, want 6 class objects retaining 24", idSize, c)
			}
		}
	}
}
