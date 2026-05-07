package heap

import "testing"

func TestParseHistogram(t *testing.T) {
	input := ` num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:          321       1048576  [B (java.base@21.0.2)
   2:           12          4096  java.lang.String (java.base@21.0.2)
   3:            7          2048  com.example.Cache$Entry
Total          340       1054720
`
	got, err := ParseHistogram(input)
	if err != nil {
		t.Fatalf("ParseHistogram: %v", err)
	}
	if len(got.Entries) != 3 {
		t.Fatalf("entries = %d, want 3: %#v", len(got.Entries), got.Entries)
	}
	first := got.Entries[0]
	if first.Rank != 1 || first.Instances != 321 || first.Bytes != 1048576 || first.ClassName != "[B" || first.Module != "java.base@21.0.2" {
		t.Fatalf("first entry = %#v", first)
	}
	if got.Total.Instances != 340 || got.Total.Bytes != 1054720 {
		t.Fatalf("total = %#v", got.Total)
	}
	if len(got.Skipped) != 0 {
		t.Fatalf("skipped = %#v", got.Skipped)
	}
}

func TestJVMHistogramImplementsGenericSnapshot(t *testing.T) {
	snapshot, err := JVMHistogramParser{}.ParseSnapshot([]byte(` num     #instances         #bytes  class name
   1:            2            64  java.lang.Object
Total            2            64
`))
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}
	if snapshot.Runtime() != RuntimeJVM || snapshot.Source() != "jcmd GC.class_histogram" {
		t.Fatalf("snapshot metadata = %s %q", snapshot.Runtime(), snapshot.Source())
	}
	records := snapshot.Records()
	if len(records) != 1 || records[0].Identity() != "java.lang.Object" || records[0].ShallowBytes() != 64 {
		t.Fatalf("records = %#v", records)
	}
	if snapshot.TotalShallowBytes() != 64 {
		t.Fatalf("total = %d, want 64", snapshot.TotalShallowBytes())
	}
}

func TestParseHistogramSkipsNonRows(t *testing.T) {
	input := `before full gc
 num     #instances         #bytes  class name
not a row
   1:            2            64  java.lang.Object
`
	got, err := ParseHistogram(input)
	if err != nil {
		t.Fatalf("ParseHistogram: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	if len(got.Skipped) != 2 {
		t.Fatalf("skipped = %d, want 2: %#v", len(got.Skipped), got.Skipped)
	}
}

func TestCompareHistogramsSortsGrowthFirst(t *testing.T) {
	before, err := ParseHistogram(` num     #instances         #bytes  class name
   1:           10          1000  com.example.Stable
   2:            5           500  com.example.Grow
   3:            2           200  com.example.Gone
`)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ParseHistogram(` num     #instances         #bytes  class name
   1:           20          2500  com.example.Grow
   2:           10          1000  com.example.Stable
   3:            1            64  com.example.New
`)
	if err != nil {
		t.Fatal(err)
	}
	got := CompareHistograms(before, after)
	if len(got) != 4 {
		t.Fatalf("deltas = %d, want 4: %#v", len(got), got)
	}
	if got[0].ClassName != "com.example.Grow" || got[0].DeltaBytes != 2000 || got[0].DeltaCount != 15 {
		t.Fatalf("first delta = %#v", got[0])
	}
	if got[len(got)-1].ClassName != "com.example.Gone" || got[len(got)-1].DeltaBytes != -200 {
		t.Fatalf("last delta = %#v", got[len(got)-1])
	}
}

func TestRankSuspects(t *testing.T) {
	before, _ := ParseHistogram(` num     #instances         #bytes  class name
   1:           10          1000  a.Big
   2:            5           500  b.Small
`)
	after, _ := ParseHistogram(` num     #instances         #bytes  class name
   1:           20          3000  a.Big
   2:           80          1600  c.Growing
   3:            1             8  b.Small
`)
	largest := RankHistogramSuspects(after, 2)
	if len(largest) != 2 || largest[0].ClassName != "a.Big" || largest[1].ClassName != "c.Growing" {
		t.Fatalf("largest = %#v", largest)
	}
	growth := RankGrowthSuspects(before, after, 2)
	if len(growth) != 2 || growth[0].ClassName != "a.Big" || growth[0].DeltaBytes != 2000 || growth[1].ClassName != "c.Growing" {
		t.Fatalf("growth = %#v", growth)
	}
}
