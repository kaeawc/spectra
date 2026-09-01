package flamegraph

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

const (
	svgWidth  = 1200
	rowHeight = 16
	padding   = 12
	minLabelW = 40
)

// node is a merged flamegraph tree node.
type node struct {
	sym      string
	count    int
	children map[string]*node
	order    []string
}

func newNode(sym string) *node {
	return &node{sym: sym, children: map[string]*node{}}
}

func (n *node) child(sym string) *node {
	c, ok := n.children[sym]
	if !ok {
		c = newNode(sym)
		n.children[sym] = c
		n.order = append(n.order, sym)
	}
	return c
}

// RenderSVG builds a self-contained SVG flamegraph from folded stacks. Output is
// deterministic: the same folded input always yields the same SVG.
func RenderSVG(folded []Folded, title string) string {
	root := newNode("root")
	for _, f := range folded {
		root.count += f.Count
		cur := root
		for _, frame := range f.Frames {
			c := cur.child(frame)
			c.count += f.Count
			cur = c
		}
	}
	if root.count == 0 {
		root.count = 1 // avoid divide-by-zero on an empty graph
	}

	depth := treeDepth(root) // excludes the synthetic root
	height := depth*rowHeight + 2*padding + rowHeight
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="Menlo,Consolas,monospace" font-size="11">`,
		svgWidth, height, svgWidth, height)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, svgWidth, height)
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="13" fill="#111">%s</text>`, padding, padding+2, escapeXML(title))

	usableW := float64(svgWidth - 2*padding)
	// The synthetic root's children form the bottom row (depth 0).
	renderRow(&b, root, float64(padding), usableW, 0, depth, root.count, height)
	b.WriteString(`</svg>`)
	return b.String()
}

// renderRow lays out a node's children left-to-right, widths proportional to
// sample count, and recurses. depth grows upward from the bottom.
func renderRow(b *strings.Builder, parent *node, x, width float64, depth, maxDepth, total, height int) {
	if len(parent.order) == 0 {
		return
	}
	childX := x
	for _, sym := range sortedChildren(parent) {
		c := parent.children[sym]
		w := width * float64(c.count) / float64(parent.count)
		y := float64(height-padding) - float64(depth+1)*float64(rowHeight)
		drawFrame(b, c, childX, y, w, total)
		renderRow(b, c, childX, w, depth+1, maxDepth, total, height)
		childX += w
	}
}

func drawFrame(b *strings.Builder, n *node, x, y, w float64, total int) {
	if w < 0.4 {
		return // too thin to see
	}
	pct := 100 * float64(n.count) / float64(total)
	fmt.Fprintf(b, `<g><title>%s (%d samples, %.1f%%)</title><rect x="%.2f" y="%d" width="%.2f" height="%d" fill="%s" stroke="#ffffff" stroke-width="0.5"/>`,
		escapeXML(n.sym), n.count, pct, x, int(y), w, rowHeight-1, warmColor(n.sym))
	if w >= minLabelW {
		fmt.Fprintf(b, `<text x="%.2f" y="%d" fill="#000">%s</text>`, x+2, int(y)+rowHeight-4, escapeXML(clip(n.sym, int(w/6))))
	}
	b.WriteString(`</g>`)
}

func sortedChildren(n *node) []string {
	c := append([]string(nil), n.order...)
	sort.SliceStable(c, func(i, j int) bool {
		if n.children[c[i]].count != n.children[c[j]].count {
			return n.children[c[i]].count > n.children[c[j]].count
		}
		return c[i] < c[j]
	})
	return c
}

func treeDepth(n *node) int {
	deepest := 0
	for _, c := range n.children {
		if d := treeDepth(c); d > deepest {
			deepest = d
		}
	}
	if n.sym == "root" {
		return deepest
	}
	return deepest + 1
}

// warmColor maps a symbol to a stable flamegraph-style warm color.
func warmColor(sym string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sym))
	v := h.Sum32()
	r := 205 + int(v%50)
	g := int((v >> 8) % 230)
	bl := int((v >> 16) % 55)
	return fmt.Sprintf("#%02x%02x%02x", r, g, bl)
}

func clip(s string, n int) string {
	if n < 1 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
