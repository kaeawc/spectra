package gojsonvectors

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite crates/base/testdata/gojson-vectors.json")

const vectorsPath = "../../crates/base/testdata/gojson-vectors.json"

// floatCase is one float and the text encoding/json writes for it. Bits carries
// the exact value (math.Float64bits or math.Float32bits) as a decimal string.
type floatCase struct {
	Bits string `json:"bits"`
	Want string `json:"want"`
}

// stringCase is one string and the JSON literal encoding/json writes for it.
type stringCase struct {
	In   string `json:"in"`
	Want string `json:"want"`
}

// structCase is one value of the mirrored sample struct, written three ways.
type structCase struct {
	Name    string `json:"name"`
	Compact string `json:"compact"`
	Indent  string `json:"indent"`
	Encoder string `json:"encoder"`
}

type vectors struct {
	Float64 []floatCase  `json:"float64"`
	Float32 []floatCase  `json:"float32"`
	Strings []stringCase `json:"strings"`
	Structs []structCase `json:"structs"`
}

// inner and sample are mirrored field for field by the Rust test in
// crates/base/tests/gojson_vectors.rs; change both together.
type inner struct {
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

type sample struct {
	ID       string            `json:"id"`
	Label    string            `json:"label,omitempty"`
	Ratio    float64           `json:"ratio"`
	Small    float32           `json:"small,omitempty"`
	Enabled  bool              `json:"enabled,omitempty"`
	Tags     []string          `json:"tags"`
	Extra    []string          `json:"extra,omitempty"`
	Ptr      *inner            `json:"ptr,omitempty"`
	Nested   inner             `json:"nested"`
	NestedOE inner             `json:"nested_oe,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Counts   map[string]int    `json:"counts"`
	At       time.Time         `json:"at"`
	Elapsed  time.Duration     `json:"elapsed_ns"`
	Note     string            `json:"note,omitempty"`
	Zero     time.Time         `json:"zero,omitzero"`
	MaybePct *float64          `json:"maybe_pct,omitempty"`
}

func TestVectors(t *testing.T) {
	got := build(t)
	if *update {
		if err := os.WriteFile(vectorsPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read vectors (regenerate with -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale for this Go toolchain; regenerate with -update", filepath.Base(vectorsPath))
	}
}

func build(t *testing.T) []byte {
	t.Helper()
	v := vectors{
		Float64: float64Cases(t),
		Float32: float32Cases(t),
		Strings: stringCases(t),
		Structs: structCases(t),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func float64Cases(t *testing.T) []floatCase {
	fixed := []float64{
		0, math.Copysign(0, -1), 1, -1, 0.1, 0.2, 0.3, 1.0 / 3, 2.0 / 3, 100, 1e6, 123456789.125,
		1e-6, 9.999999999999999e-7, 1e-7, 1.5e-7, 1e-9, 1e-10, 1e-300, 5e-324, math.SmallestNonzeroFloat64,
		1e20, 9.999999999999999e20, 1e21, 1.5e21, 1e22, 1e100, 1.5e300, math.MaxFloat64,
		12345678901234567890, 0.000001234, 2.626478672027588, 99.5, -0.5, -1e21, -1e-7, 4503599627370496.5,
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		f := math.Float64frombits(r.Uint64())
		if math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		fixed = append(fixed, f)
	}
	for range 500 {
		fixed = append(fixed, r.NormFloat64()*math.Pow(10, float64(r.IntN(40)-20)))
	}
	out := make([]floatCase, 0, len(fixed))
	for _, f := range fixed {
		out = append(out, floatCase{Bits: strconv.FormatUint(math.Float64bits(f), 10), Want: mustMarshal(t, f)})
	}
	return out
}

func float32Cases(t *testing.T) []floatCase {
	fixed := []float32{
		0, float32(math.Copysign(0, -1)), 1, 0.1, 1.0 / 3, 16777216, 1e-6, 9.9999e-7, 1e-7, 1e20, 1e21,
		3.4e38, math.MaxFloat32, math.SmallestNonzeroFloat32, 2.626478672027588,
	}
	r := rand.New(rand.NewPCG(3, 4))
	for range 1000 {
		f := math.Float32frombits(r.Uint32())
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			continue
		}
		fixed = append(fixed, f)
	}
	out := make([]floatCase, 0, len(fixed))
	for _, f := range fixed {
		out = append(out, floatCase{Bits: strconv.FormatUint(uint64(math.Float32bits(f)), 10), Want: mustMarshal(t, f)})
	}
	return out
}

func stringCases(t *testing.T) []stringCase {
	ins := []string{
		"", "plain", `quote " and backslash \`, "<script>&amp;</script>", "a\u2028b\u2029c",
		"tab\tnl\ncr\rbs\bff\f", "\x00\x01\x1f\x7f", "é ü 日本 🎉", "\ufffd", "/slash/", "'single'",
	}
	for r := rune(0); r < 0x80; r++ {
		ins = append(ins, string(r))
	}
	rng := rand.New(rand.NewPCG(5, 6))
	for range 300 {
		rs := make([]rune, 1+rng.IntN(8))
		for i := range rs {
			c := rune(rng.IntN(0x10FFFF))
			if c >= 0xD800 && c <= 0xDFFF {
				c = 'x'
			}
			rs[i] = c
		}
		ins = append(ins, string(rs))
	}
	out := make([]stringCase, 0, len(ins))
	for _, s := range ins {
		out = append(out, stringCase{In: s, Want: mustMarshal(t, s)})
	}
	return out
}

func structCases(t *testing.T) []structCase {
	pct := 0.0
	ist := time.FixedZone("IST", 5*3600+1800)
	cases := []struct {
		name string
		v    sample
	}{
		{"zero", sample{}},
		{"full", sample{
			ID: "id-1", Label: "Label <b>&</b>", Ratio: 0.75, Small: 1.5, Enabled: true,
			Tags: []string{"a", "b"}, Extra: []string{"x"}, Ptr: &inner{Name: "p", Count: 2},
			Nested: inner{Name: "n", Count: 3}, NestedOE: inner{Name: "oe"},
			Attrs:  map[string]string{"zeta": "z", "alpha": "a", "<k>": "v&"},
			Counts: map[string]int{"b": 2, "a": 1, "10": 10, "9": 9},
			At:     time.Date(2026, 11, 27, 13, 15, 0, 120000000, time.UTC), Elapsed: 1500 * time.Millisecond,
			Note: "n", Zero: time.Date(2026, 1, 2, 3, 4, 5, 6, ist), MaybePct: &pct,
		}},
		{"empty_not_nil", sample{
			ID: "id-2", Ratio: 1e21, Tags: []string{}, Extra: []string{}, Attrs: map[string]string{},
			Counts: map[string]int{}, At: time.Date(2026, 11, 27, 18, 45, 30, 0, ist), Elapsed: -1,
		}},
	}
	out := make([]structCase, 0, len(cases))
	for _, c := range cases {
		indent, err := json.MarshalIndent(c.v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		var enc bytes.Buffer
		if err := json.NewEncoder(&enc).Encode(c.v); err != nil {
			t.Fatal(err)
		}
		out = append(out, structCase{Name: c.name, Compact: mustMarshal(t, c.v), Indent: string(indent), Encoder: enc.String()})
	}
	return out
}
