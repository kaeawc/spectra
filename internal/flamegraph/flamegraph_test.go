package flamegraph

import (
	"strings"
	"testing"
)

// A small call graph: main splits into two leaves with different self time,
// plus main has some self time of its own.
const sampleGraph = `Analysis of sampling ...

Call graph:
    100 Thread_1   DispatchQueue_1: main-thread  (serial)
      100 start  (in dyld) + 10  [0x1]
        100 main  (in App) + 20  [0x2]
          60 work  (in App) + 4  [0x3]
          30 idle  (in App) + 8  [0x4]

Total number in stack:
      100 something else
`

func TestFoldSelfCounts(t *testing.T) {
	folded := Fold(sampleGraph)
	// Expect three folded stacks: work (60), idle (30), and main's self (10).
	byLeaf := map[string]Folded{}
	for _, f := range folded {
		byLeaf[f.Frames[len(f.Frames)-1]] = f
	}
	if got := byLeaf["work"].Count; got != 60 {
		t.Errorf("work self = %d, want 60", got)
	}
	if got := byLeaf["idle"].Count; got != 30 {
		t.Errorf("idle self = %d, want 30", got)
	}
	if got := byLeaf["main"].Count; got != 10 {
		t.Errorf("main self = %d, want 10 (100 - 60 - 30)", got)
	}
	// The full path to a leaf is root→leaf, symbols cleaned.
	if strings.Join(byLeaf["work"].Frames, ";") != "Thread_1;start;main;work" {
		t.Errorf("work path = %v", byLeaf["work"].Frames)
	}
}

func TestFoldIgnoresNonGraph(t *testing.T) {
	if f := Fold("no call graph here\njust text\n"); len(f) != 0 {
		t.Errorf("expected no stacks, got %v", f)
	}
}

func TestCleanSymbol(t *testing.T) {
	cases := map[string]string{
		"main  (in App) + 20  [0x2]":                               "main",
		"start  (in dyld) + 6076  [0x19037eb98]":                   "start",
		"???  (in zsh)  load address 0x1 + 0x2  [0x3]":             "???",
		"Thread_75376767   DispatchQueue_1: com.apple.main-thread": "Thread_75376767",
	}
	for in, want := range cases {
		if got := cleanSymbol(in); got != want {
			t.Errorf("cleanSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderSVGDeterministicAndSafe(t *testing.T) {
	folded := Fold(sampleGraph)
	a := RenderSVG(folded, "t")
	b := RenderSVG(folded, "t")
	if a != b {
		t.Error("RenderSVG is not deterministic")
	}
	if !strings.HasPrefix(a, "<svg") || !strings.Contains(a, "</svg>") {
		t.Error("output is not an SVG")
	}
	if !strings.Contains(a, "<title>work (60 samples") {
		t.Errorf("expected a work frame title, got:\n%s", a)
	}
	// No external references or scripts (CSP-safe). The xmlns namespace URI is
	// a required, non-fetched identifier, so it is not a violation.
	for _, bad := range []string{"href", "src=", "<script", "<image", "url(", "<foreignObject"} {
		if strings.Contains(a, bad) {
			t.Errorf("SVG contains external/script reference %q", bad)
		}
	}
}

func TestRenderSVGEscapesXML(t *testing.T) {
	folded := []Folded{{Frames: []string{"a<b>&\"c"}, Count: 5}}
	svg := RenderSVG(folded, "x")
	if strings.Contains(svg, "<b>") {
		t.Errorf("symbol angle brackets not escaped:\n%s", svg)
	}
}
