package mallochistory

import "testing"

const allBySizeOut = `Malloc stack logging: recording malloc and VM allocation stacks
8 calls for 131072 bytes: thread_7fff | start | main | foo | malloc
16 calls for 262144 bytes: thread_7fff | start | main | bar | operator new
2 calls for 4096 bytes: thread_7fff | start | helper | calloc
`

func TestParseAllBySizeRankedByBytes(t *testing.T) {
	sites := ParseAllBySize(allBySizeOut, 0)
	if len(sites) != 3 {
		t.Fatalf("sites = %d, want 3", len(sites))
	}
	// Ranked by bytes: 262144 first.
	if sites[0].Bytes != 262144 || sites[0].Calls != 16 {
		t.Errorf("top site = %+v", sites[0])
	}
	if sites[0].Stack[len(sites[0].Stack)-1] != "operator new" {
		t.Errorf("leaf frame = %q", sites[0].Stack[len(sites[0].Stack)-1])
	}
}

func TestParseAllBySizeTopN(t *testing.T) {
	if sites := ParseAllBySize(allBySizeOut, 2); len(sites) != 2 {
		t.Errorf("topN=2 gave %d", len(sites))
	}
}

func TestParseAddress(t *testing.T) {
	out := `ALLOC 0x600000abcdef-0x600000abce00 [size=17]: thread_1 | start | main | leak | malloc
FREE  0x600000abcdef: thread_1 | start | teardown | free
`
	traces := ParseAddress(out)
	if len(traces) != 2 {
		t.Fatalf("traces = %d, want 2", len(traces))
	}
	if traces[0].Kind != "ALLOC" || traces[0].Frames[len(traces[0].Frames)-1] != "malloc" {
		t.Errorf("alloc trace = %+v", traces[0])
	}
	if traces[1].Kind != "FREE" {
		t.Errorf("free trace kind = %q", traces[1].Kind)
	}
}

func TestStackLoggingDisabled(t *testing.T) {
	disabled := []string{
		"malloc_history: stack logging not enabled for this process",
		"Malloc stack logging was not enabled",
		"error: no malloc stack logging",
	}
	for _, s := range disabled {
		if !StackLoggingDisabled(s) {
			t.Errorf("expected disabled for %q", s)
		}
	}
	if StackLoggingDisabled(allBySizeOut) {
		t.Error("a real recording must not be flagged disabled")
	}
}

func TestSplitStackSanitizes(t *testing.T) {
	frames := splitStack("start | ma\x1bin |  | leak\x07 ")
	// Empty segment dropped; control bytes stripped.
	if len(frames) != 3 {
		t.Fatalf("frames = %v", frames)
	}
	for _, f := range frames {
		for _, r := range f {
			if r < 0x20 {
				t.Errorf("control byte survived in %q", f)
			}
		}
	}
}
