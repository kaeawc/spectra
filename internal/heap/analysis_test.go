package heap

import "testing"

type testRecord struct {
	id    string
	name  string
	count int64
	bytes int64
}

func (r testRecord) Identity() string    { return r.id }
func (r testRecord) DisplayName() string { return r.name }
func (r testRecord) LiveCount() int64    { return r.count }
func (r testRecord) ShallowBytes() int64 { return r.bytes }

type testSnapshot struct {
	runtime Runtime
	source  string
	records []Record
	total   int64
}

func (s testSnapshot) Runtime() Runtime         { return s.runtime }
func (s testSnapshot) Source() string           { return s.source }
func (s testSnapshot) Records() []Record        { return s.records }
func (s testSnapshot) TotalShallowBytes() int64 { return s.total }

func TestDefaultAnalyzerWorksAcrossRuntimeRecords(t *testing.T) {
	before := testSnapshot{
		runtime: RuntimeNative,
		source:  "malloc-history",
		records: []Record{
			testRecord{id: "alloc:cache", name: "Cache allocations", count: 10, bytes: 1024},
			testRecord{id: "alloc:old", name: "Old allocations", count: 1, bytes: 512},
		},
	}
	after := testSnapshot{
		runtime: RuntimeNative,
		source:  "malloc-history",
		records: []Record{
			testRecord{id: "alloc:cache", name: "Cache allocations", count: 14, bytes: 4096},
			testRecord{id: "alloc:new", name: "New allocations", count: 2, bytes: 2048},
		},
	}

	analyzer := DefaultAnalyzer{}
	deltas, err := analyzer.Compare(before, after)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3: %#v", len(deltas), deltas)
	}
	if deltas[0].Identity != "alloc:cache" || deltas[0].Runtime != RuntimeNative || deltas[0].DeltaBytes != 3072 {
		t.Fatalf("first delta = %#v", deltas[0])
	}

	largest := analyzer.RankLargest(after, 1)
	if len(largest) != 1 || largest[0].Identity != "alloc:cache" {
		t.Fatalf("largest = %#v", largest)
	}

	growth, err := analyzer.RankGrowth(before, after, 2)
	if err != nil {
		t.Fatalf("RankGrowth: %v", err)
	}
	if len(growth) != 2 || growth[0].Identity != "alloc:cache" || growth[1].Identity != "alloc:new" {
		t.Fatalf("growth = %#v", growth)
	}
}

func TestDefaultAnalyzerRejectsNilSnapshots(t *testing.T) {
	_, err := DefaultAnalyzer{}.Compare(nil, testSnapshot{})
	if err == nil {
		t.Fatal("expected nil snapshot error")
	}
}
