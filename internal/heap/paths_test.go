package heap

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// rootRecord writes a ROOT_* sub-record: the object id followed by u4 fields
// (thread serial, frame/stack numbers) as the tag requires.
func (b *hprofBuilder) rootRecord(w *bytes.Buffer, tag byte, objID uint64, u4s ...uint32) {
	w.WriteByte(tag)
	b.writeIDTo(w, objID)
	for _, v := range u4s {
		_ = binary.Write(w, binary.BigEndian, v)
	}
}

func TestParseGCRootKinds(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		s := newSynthDump(idSize)
		standardClasses(s)
		s.obj(10, clsA)
		s.obj(11, clsA)
		s.obj(12, clsA)
		s.obj(13, clsA)
		s.obj(50, clsB) // the thread object
		// Java frame root before the thread object root that names its serial.
		s.b.rootRecord(&s.seg, hprofRootJavaFrame, 10, 7, 0)
		s.b.rootRecord(&s.seg, hprofRootThreadObject, 50, 7, 1)
		s.b.rootRecord(&s.seg, hprofRootNativeStack, 11, 7)
		s.b.rootRecord(&s.seg, hprofRootMonitorUsed, 12)
		s.b.rootRecord(&s.seg, hprofRootJNILocal, 13, 9, 0) // unknown thread serial
		s.root(10)                                          // JNI global: a second root record for 10
		snap := s.parse(t)
		g := snap.Graph

		want := map[uint64][]GCRoot{
			10:   {{Kind: RootJavaFrame, ThreadID: 50}, {Kind: RootJNIGlobal}},
			11:   {{Kind: RootNativeStack, ThreadID: 50}},
			12:   {{Kind: RootMonitorUsed}},
			13:   {{Kind: RootJNILocal}},
			50:   {{Kind: RootThreadObject}},
			clsA: {{Kind: RootStickyClass}},
		}
		for id, w := range want {
			i, _ := g.Index(id)
			got := g.RootsOf(i)
			if len(got) != len(w) {
				t.Errorf("idSize %d: roots of 0x%x = %+v, want %+v", idSize, id, got, w)
				continue
			}
			for k := range w {
				if got[k].Kind != w[k].Kind || got[k].ThreadID != w[k].ThreadID {
					t.Errorf("idSize %d: root %d of 0x%x = %+v, want %+v", idSize, k, id, got[k], w[k])
				}
			}
		}
		if len(g.Roots()) != 5+5 { // five objects + five sticky classes, deduplicated
			t.Errorf("idSize %d: distinct roots = %d, want 10", idSize, len(g.Roots()))
		}
	}
}

func TestPathsToGCRoots(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		s := newSynthDump(idSize)
		standardClasses(s)
		// class Holder --static--> h(20) -> l(21) -> n1(22) -> target(23)
		// frame root f(30) -> target(23)   (the shorter path)
		s.class(9, "com/acme/Holder", 0, 20)
		s.obj(20, clsA, 21)
		s.obj(21, clsB, 22)
		s.obj(22, clsC, 23)
		s.obj(23, clsD)
		s.obj(30, clsE, 23)
		s.b.rootRecord(&s.seg, hprofRootJavaFrame, 30, 1, 0)
		g := s.parse(t).Graph

		target, _ := g.Index(23)
		paths := g.PathsToGCRoots([]int32{target}, 5)
		if len(paths) != 2 {
			t.Fatalf("idSize %d: got %d paths, want 2: %+v", idSize, len(paths), paths)
		}
		if got := stepIDs(paths[0]); !equalIDs(got, []uint64{30, 23}) || paths[0].Roots[0].Kind != RootJavaFrame {
			t.Errorf("idSize %d: shortest path = %v %+v, want [30 23] via java_frame", idSize, got, paths[0].Roots)
		}
		if got := stepIDs(paths[1]); !equalIDs(got, []uint64{9, 20, 21, 22, 23}) || paths[1].Roots[0].Kind != RootStickyClass {
			t.Errorf("idSize %d: second path = %v %+v, want via class Holder", idSize, got, paths[1].Roots)
		}
		if !strings.HasPrefix(paths[1].String(), "[sticky_class] class com.acme.Holder (0x9) → A (0x14)") {
			t.Errorf("idSize %d: rendered path = %q", idSize, paths[1].String())
		}
		if one := g.PathsToGCRoots([]int32{target}, 1); len(one) != 1 {
			t.Errorf("idSize %d: maxPaths=1 gave %d paths", idSize, len(one))
		}
		// A class query starts from every instance at once.
		byClass := g.PathsToGCRoots(g.InstancesOf("C"), 1)
		if len(byClass) != 1 || !equalIDs(stepIDs(byClass[0]), []uint64{9, 20, 21, 22}) {
			t.Errorf("idSize %d: class C path = %+v", idSize, byClass)
		}
	}
}

func TestPathsToGCRootsUnreachable(t *testing.T) {
	s := newSynthDump(8)
	standardClasses(s)
	s.obj(40, clsA, 41)
	s.obj(41, clsB)
	g := s.parse(t).Graph
	orphan, _ := g.Index(41)
	if paths := g.PathsToGCRoots([]int32{orphan}, 3); len(paths) != 0 {
		t.Errorf("unreachable object has paths: %+v", paths)
	}
}

func stepIDs(p RootPath) []uint64 {
	ids := make([]uint64, len(p.Steps))
	for i, st := range p.Steps {
		ids[i] = st.ID
	}
	return ids
}
