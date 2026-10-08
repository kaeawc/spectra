package heap

import (
	"strings"
	"testing"
)

// buildCacheLeak models a static cache: class Cache --static--> map(100) ->
// table(101, Entry[50]) -> entry(200+i) -> byte[1000] value(300+i), plus small
// independently rooted noise objects.
func buildCacheLeak(idSize int) *synthDump {
	s := newSynthDump(idSize)
	s.class(1, "com/acme/Cache", 0, 100)
	s.class(2, "com/acme/Map", 24)
	s.class(3, "com/acme/Entry", 16)
	s.class(4, "[Lcom/acme/Entry;", 0)
	s.class(5, "com/acme/Noise", 8)
	s.obj(100, 2, 101)
	var entries []uint64
	for i := uint64(0); i < 50; i++ {
		entries = append(entries, 200+i)
		s.obj(200+i, 3, 300+i)
		s.b.primArrayDump(&s.seg, 300+i, 1000, hprofTypeByte, 1)
	}
	s.b.objectArrayElems(&s.seg, 101, 4, entries...)
	for i := uint64(0); i < 20; i++ {
		s.obj(500+i, 5)
		s.root(500 + i)
	}
	return s
}

func TestLeakSuspectsStaticCache(t *testing.T) {
	for _, idSize := range []int{8, 4} {
		snap := buildCacheLeak(idSize).parse(t)
		rep := LeakSuspects(snap, LeakOptions{})
		if len(rep.Suspects) != 1 {
			t.Fatalf("idSize %d: suspects = %+v, want 1", idSize, rep.Suspects)
		}
		sus := rep.Suspects[0]
		table := int64(hprofArrayHeaderBytes + 50*idSize)
		wantRetained := 24 + table + 50*(16+hprofArrayHeaderBytes+1000)
		if sus.Kind != LeakSuspectObject || sus.ClassName != "class com.acme.Cache" || sus.RetainedBytes != wantRetained {
			t.Errorf("idSize %d: suspect = %+v, want class com.acme.Cache retaining %d", idSize, sus, wantRetained)
		}
		acc := sus.Accumulation
		if acc == nil || acc.ID != 101 || acc.ClassName != "[Lcom.acme.Entry;" || acc.DominatedObjects != 50 || acc.Depth != 2 {
			t.Errorf("idSize %d: accumulation = %+v, want the Entry[] table (0x65) dominating 50 entries at depth 2", idSize, acc)
		}
		if p := sus.PathToRoot; p == nil || !equalIDs(stepIDs(*p), []uint64{1, 100, 101}) || p.Roots[0].Kind != RootStickyClass {
			t.Errorf("idSize %d: path = %+v, want class Cache → map → table", idSize, p)
		}
		for _, want := range []string{"class com.acme.Cache (0x1) retains", "accumulates in [Lcom.acme.Entry; (0x65)", "across 50 directly dominated objects", "[sticky_class]"} {
			if !strings.Contains(sus.Description, want) {
				t.Errorf("idSize %d: description missing %q: %s", idSize, want, sus.Description)
			}
		}
	}
}

func TestLeakSuspectsClassGroup(t *testing.T) {
	// 30 sessions, each rooted separately and holding a 1000-byte buffer: no one
	// session reaches 10%, but together they hold almost the whole heap.
	s := newSynthDump(8)
	s.class(1, "com/acme/Session", 32)
	s.class(2, "com/acme/Small", 8)
	for i := uint64(0); i < 30; i++ {
		s.obj(100+i, 1, 200+i)
		s.b.primArrayDump(&s.seg, 200+i, 1000, hprofTypeByte, 1)
		s.root(100 + i)
	}
	s.obj(400, 2)
	s.root(400)
	rep := LeakSuspects(s.parse(t), LeakOptions{})
	if len(rep.Suspects) != 1 {
		t.Fatalf("suspects = %+v, want one class group", rep.Suspects)
	}
	sus := rep.Suspects[0]
	if sus.Kind != LeakSuspectClass || sus.ClassName != "com.acme.Session" || sus.Instances != 30 ||
		sus.RetainedBytes != 30*(32+hprofArrayHeaderBytes+1000) {
		t.Errorf("class suspect = %+v", sus)
	}
	if sus.PathToRoot == nil || sus.PathToRoot.Roots[0].Kind != RootJNIGlobal || !strings.Contains(sus.Description, "30 instances of com.acme.Session") {
		t.Errorf("class suspect path/description = %+v / %s", sus.PathToRoot, sus.Description)
	}
}

func TestLeakSuspectsThresholdAndEmpty(t *testing.T) {
	snap := buildCacheLeak(8).parse(t)
	if rep := LeakSuspects(snap, LeakOptions{ThresholdPercent: 100}); len(rep.Suspects) != 0 {
		t.Errorf("100%% threshold still reported %+v", rep.Suspects)
	}
	if rep := LeakSuspects(ComputeRetained(graph(nil)), LeakOptions{}); len(rep.Suspects) != 0 || rep.ThresholdPercent != DefaultLeakThresholdPercent {
		t.Errorf("empty heap report = %+v", rep)
	}
}
