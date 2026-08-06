package corefile

import "testing"

func TestSelectCommand(t *testing.T) {
	report := Report{Commands: []Command{
		{Tool: "jhsdb", Args: []string{"jstack", "--exe", "/j", "--core", "/c"}},
		{Tool: "jhsdb", Args: []string{"jmap", "--histo", "--exe", "/j", "--core", "/c"}},
		{Tool: "jhsdb", Args: []string{"jmap", "--binaryheap", "--dumpfile", "<heap.hprof>"}},
	}}

	if c, ok := SelectCommand(report, "jstack"); !ok || c.Args[0] != "jstack" {
		t.Fatalf("jstack select = %+v ok=%v", c, ok)
	}
	if c, ok := SelectCommand(report, "jmap-histo"); !ok || c.Args[1] != "--histo" {
		t.Fatalf("jmap-histo select = %+v ok=%v", c, ok)
	}
	if _, ok := SelectCommand(report, "jstat"); ok {
		t.Fatal("jstat should not select any command")
	}
	if _, ok := SelectCommand(Report{}, "jstack"); ok {
		t.Fatal("empty report should not select")
	}
}
