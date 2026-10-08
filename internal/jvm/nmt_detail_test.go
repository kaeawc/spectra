package jvm

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func readNMTFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func TestParseNMTDetailJDK17(t *testing.T) {
	d := ParseNMTDetail(readNMTFixture(t, "nmt_detail_jdk17.txt"))
	if !d.Summary.Enabled || d.Summary.TotalCommittedKiB != 340142 || len(d.Summary.Categories) != 9 {
		t.Fatalf("summary = %+v", d.Summary)
	}
	if len(d.Sites) != 5 {
		t.Fatalf("sites = %d, want 5: %+v", len(d.Sites), d.Sites)
	}
	top := d.Sites[0]
	if top.Kind != NMTSiteMmap || top.Category != "Java Heap" || top.ReservedKiB != 4194304 || top.CommittedKiB != 262144 {
		t.Errorf("top site = %+v", top)
	}
	if len(top.Frames) != 4 || top.Frames[1] != "Universe::reserve_heap(unsigned long, unsigned long)+0x83" {
		t.Errorf("top frames = %q", top.Frames)
	}
	unsafe := d.Sites[1]
	wantFrames := []string{"Unsafe_AllocateMemory0+0x84", "0x00007f2f5101a3f6"}
	if unsafe.Kind != NMTSiteMalloc || unsafe.Category != "Other" || unsafe.CommittedKiB != 24576 || unsafe.Count != 6 || !reflect.DeepEqual(unsafe.Frames, wantFrames) {
		t.Errorf("unsafe site = %+v", unsafe)
	}
	if last := d.Sites[len(d.Sites)-1]; last.Category != "Thread" || last.CommittedKiB != 12 || len(last.Frames) != 3 {
		t.Errorf("last site = %+v", last)
	}
}

func TestParseNMTDetailJDK17Regions(t *testing.T) {
	d := ParseNMTDetail(readNMTFixture(t, "nmt_detail_jdk17.txt"))
	if len(d.Regions) != 3 {
		t.Fatalf("regions = %d, want 3: %+v", len(d.Regions), d.Regions)
	}
	heap := d.Regions[0]
	if heap.Category != "Java Heap" || heap.Start != "0x0000000700000000" || heap.ReservedKiB != 4194304 || heap.CommittedKiB != 262144 {
		t.Errorf("heap region = %+v", heap)
	}
	if len(heap.Frames) != 4 || len(heap.Committed) != 1 || len(heap.Committed[0].Frames) != 4 {
		t.Errorf("heap region frames = %d, committed = %+v", len(heap.Frames), heap.Committed)
	}
	if heap.Committed[0].Frames[0] != "G1PageBasedVirtualSpace::commit(unsigned long, unsigned long)+0x14f" {
		t.Errorf("committed frame = %q", heap.Committed[0].Frames[0])
	}
	stack := d.Regions[2]
	if stack.Category != "Thread Stack" || stack.ReservedKiB != 132 || stack.CommittedKiB != 132 || len(stack.Frames) != 2 {
		t.Errorf("stack region = %+v", stack)
	}
}

func TestParseNMTDetailJDK21(t *testing.T) {
	d := ParseNMTDetail(readNMTFixture(t, "nmt_detail_jdk21.txt"))
	if d.Summary.TotalReservedKiB != 1772146 || len(d.Summary.Categories) != 9 {
		t.Fatalf("summary = %+v", d.Summary)
	}
	if len(d.Sites) != 3 {
		t.Fatalf("sites = %+v", d.Sites)
	}
	if s := d.Sites[1]; s.Category != "Internal" || s.CommittedKiB != 32768 || s.Count != 4 {
		t.Errorf("internal site = %+v", s)
	}
	if s := d.Sites[2]; s.Category != "Compiler" || s.CommittedKiB != 1024 || s.Count != 2 || len(s.Frames) != 3 {
		t.Errorf("compiler site = %+v", s)
	}
	if len(d.Regions) != 1 || d.Regions[0].CommittedKiB != 65536 {
		t.Errorf("regions = %+v", d.Regions)
	}
}

func TestParseNMTDetailDisabled(t *testing.T) {
	d := ParseNMTDetail("1234:\nNative memory tracking is not enabled\n")
	if d.Summary.Enabled || d.Sites != nil || d.Regions != nil {
		t.Fatalf("detail = %+v", d)
	}
}

func TestNativeMemoryRunsJcmd(t *testing.T) {
	var got []string
	run := func(name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("ok"), nil
	}
	if _, err := NativeMemory(42, "detail", run); err != nil {
		t.Fatal(err)
	}
	if want := []string{"jcmd", "42", "VM.native_memory", "detail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
}
