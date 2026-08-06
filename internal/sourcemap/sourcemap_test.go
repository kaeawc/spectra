package sourcemap

import "testing"

func eqInts(a, b []int) bool {
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

func TestDecodeVLQ(t *testing.T) {
	cases := map[string][]int{
		"A":    {0},
		"C":    {1},
		"D":    {-1},
		"gB":   {16},
		"AAAA": {0, 0, 0, 0},
		"AACA": {0, 0, 1, 0},
		"CAAC": {1, 0, 0, 1},
	}
	for in, want := range cases {
		got, err := decodeVLQ(in)
		if err != nil {
			t.Fatalf("decodeVLQ(%q): %v", in, err)
		}
		if !eqInts(got, want) {
			t.Errorf("decodeVLQ(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDecodeVLQBadChar(t *testing.T) {
	if _, err := decodeVLQ("A!"); err == nil {
		t.Fatal("expected error for invalid base64 char")
	}
}

func TestLookupBasic(t *testing.T) {
	sm, err := Parse([]byte(`{"version":3,"sources":["orig.js"],"names":[],"mappings":"AAAA;AACA"}`))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := sm.Lookup(1, 0)
	if !ok || p.Source != "orig.js" || p.Line != 1 || p.Column != 0 {
		t.Errorf("lookup(1,0) = %+v ok=%v", p, ok)
	}
	// second generated line maps to original line 2
	p, ok = sm.Lookup(2, 5)
	if !ok || p.Line != 2 {
		t.Errorf("lookup(2,5) = %+v ok=%v, want line 2", p, ok)
	}
}

func TestLookupColumnBinarySearch(t *testing.T) {
	sm, err := Parse([]byte(`{"version":3,"sources":["a.js"],"names":[],"mappings":"AAAA,CAAC"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := sm.Lookup(1, 0); p.Column != 0 {
		t.Errorf("col at gen 0 = %d, want 0", p.Column)
	}
	if p, _ := sm.Lookup(1, 1); p.Column != 1 {
		t.Errorf("col at gen 1 = %d, want 1", p.Column)
	}
	if p, _ := sm.Lookup(1, 9); p.Column != 1 {
		t.Errorf("col at gen 9 = %d, want 1 (nearest preceding segment)", p.Column)
	}
}

func TestLookupName(t *testing.T) {
	sm, err := Parse([]byte(`{"version":3,"sources":["a.js"],"names":["myFunc"],"mappings":"AAAAA"}`))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := sm.Lookup(1, 0)
	if !ok || p.Name != "myFunc" {
		t.Errorf("lookup = %+v ok=%v, want name myFunc", p, ok)
	}
}

func TestLookupMisses(t *testing.T) {
	sm, _ := Parse([]byte(`{"version":3,"sources":["a.js"],"names":[],"mappings":"AAAA"}`))
	if _, ok := sm.Lookup(5, 0); ok {
		t.Error("lookup on a nonexistent line should miss")
	}
}

func TestSourceRootPrefixed(t *testing.T) {
	sm, _ := Parse([]byte(`{"version":3,"sourceRoot":"src/","sources":["a.js"],"names":[],"mappings":"AAAA"}`))
	if p, _ := sm.Lookup(1, 0); p.Source != "src/a.js" {
		t.Errorf("source = %q, want src/a.js", p.Source)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("expected error for bad json")
	}
	if _, err := Parse([]byte(`{"version":2,"mappings":""}`)); err == nil {
		t.Error("expected error for unsupported version")
	}
}
