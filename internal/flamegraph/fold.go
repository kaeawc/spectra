// Package flamegraph turns a native `sample` call graph into folded stacks and
// renders a self-contained SVG flamegraph. It reads only.
package flamegraph

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/binaryimages"
)

// Folded is one collapsed stack (root→leaf) and its self sample count.
// FrameDetails is parallel to Frames and present only when the report's
// Binary Images section placed at least one frame in a known image.
type Folded struct {
	Frames       []string                `json:"frames"`
	Count        int                     `json:"count"`
	FrameDetails []binaryimages.Location `json:"frame_details,omitempty"`
}

var (
	// callGraphStart marks the beginning of sample's call-tree section.
	callGraphStart = "Call graph:"
	// frameLine matches an indented "<count> <symbol...>" call-graph line. When a
	// thread's stack branches, `sample` draws the indentation with +, !, :, and |
	// tree characters rather than plain spaces, so the indent run includes them.
	frameLine  = regexp.MustCompile(`^([\s+!:|]+)(\d+)\s+(.*\S)\s*$`)
	offsetTail = regexp.MustCompile(`\s+\+\s+\d+$`)
)

// foldNode is one entry on the ancestry stack while folding.
type foldNode struct {
	indent   int
	sym      string
	loc      binaryimages.Location
	count    int
	childSum int
}

// Fold parses a `sample` report's call graph into folded stacks whose weight is
// each frame's self time (its sample count minus the sum of its children).
func Fold(sampleOutput string) []Folded {
	var folded []Folded
	var stack []foldNode
	images := binaryimages.FromReport(sampleOutput)

	pop := func() {
		n := stack[len(stack)-1]
		if self := n.count - n.childSum; self > 0 {
			folded = append(folded, foldStack(stack, self))
		}
		stack = stack[:len(stack)-1]
		if len(stack) > 0 {
			stack[len(stack)-1].childSum += n.count
		}
	}

	inGraph := false
	for _, line := range strings.Split(sampleOutput, "\n") {
		if !inGraph {
			if strings.HasPrefix(strings.TrimSpace(line), callGraphStart) {
				inGraph = true
			}
			continue
		}
		m := frameLine.FindStringSubmatch(line)
		if m == nil {
			// The call graph ends at the first non-frame line after it started.
			if strings.TrimSpace(line) != "" {
				break
			}
			continue
		}
		indent := len(m[1])
		count, _ := strconv.Atoi(m[2])
		sym := cleanSymbol(m[3])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			pop()
		}
		loc, _ := images.LookupFrame(m[3])
		stack = append(stack, foldNode{indent: indent, sym: sym, loc: loc, count: count})
	}
	for len(stack) > 0 {
		pop()
	}
	return folded
}

// foldStack copies the symbols (and, when any is known, the locations) of
// stack root→leaf.
func foldStack(stack []foldNode, count int) Folded {
	f := Folded{Frames: make([]string, len(stack)), Count: count}
	known := false
	for i, n := range stack {
		f.Frames[i] = n.sym
		known = known || !n.loc.IsZero()
	}
	if known {
		f.FrameDetails = make([]binaryimages.Location, len(stack))
		for i, n := range stack {
			f.FrameDetails[i] = n.loc
		}
	}
	return f
}

// cleanSymbol reduces a call-graph frame's text to a bare symbol name.
func cleanSymbol(s string) string {
	if i := strings.Index(s, "  (in "); i >= 0 {
		s = s[:i]
	} else if i := strings.Index(s, " (in "); i >= 0 {
		s = s[:i]
	} else if i := strings.Index(s, "   "); i >= 0 {
		// Thread header line ("Thread_123   DispatchQueue…"): keep the first col.
		s = s[:i]
	}
	if i := strings.Index(s, "  ["); i >= 0 {
		s = s[:i]
	}
	s = offsetTail.ReplaceAllString(s, "")
	return sanitize(strings.TrimSpace(s))
}

// sanitize strips terminal control bytes from text taken from the sample.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, s)
}
