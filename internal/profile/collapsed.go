package profile

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// StackSample is one weighted stack from a profiler output.
type StackSample struct {
	Frames []string `json:"frames"`
	Value  int64    `json:"value"`
}

// MethodStat is an inclusive and exclusive method ranking entry.
type MethodStat struct {
	Name      string `json:"name"`
	Inclusive int64  `json:"inclusive"`
	Exclusive int64  `json:"exclusive"`
}

// CallTreeNode is a compact call tree node usable by CLI, RPC, and TUI views.
type CallTreeNode struct {
	Name     string         `json:"name"`
	Value    int64          `json:"value"`
	Self     int64          `json:"self,omitempty"`
	Children []CallTreeNode `json:"children,omitempty"`
}

// ParseCollapsedStacks parses lines shaped like "a;b;c 42". Several profilers
// can emit this folded/collapsed format, including async-profiler and Linux
// perf tooling, so the parser stays outside runtime-specific packages.
func ParseCollapsedStacks(r io.Reader) ([]StackSample, error) {
	var samples []StackSample
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		stack, valueText, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("collapsed stack line %d missing value", lineNo)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(valueText), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("collapsed stack line %d value: %w", lineNo, err)
		}
		if value < 0 {
			return nil, fmt.Errorf("collapsed stack line %d value must be non-negative", lineNo)
		}
		frames := splitFrames(stack)
		if len(frames) == 0 {
			continue
		}
		samples = append(samples, StackSample{Frames: frames, Value: value})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

// HotMethods returns method totals sorted by inclusive value, then name.
func HotMethods(samples []StackSample) []MethodStat {
	byName := make(map[string]*MethodStat)
	for _, sample := range samples {
		seen := make(map[string]bool, len(sample.Frames))
		for i, frame := range sample.Frames {
			stat := byName[frame]
			if stat == nil {
				stat = &MethodStat{Name: frame}
				byName[frame] = stat
			}
			if !seen[frame] {
				stat.Inclusive += sample.Value
				seen[frame] = true
			}
			if i == len(sample.Frames)-1 {
				stat.Exclusive += sample.Value
			}
		}
	}
	out := make([]MethodStat, 0, len(byName))
	for _, stat := range byName {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Inclusive != out[j].Inclusive {
			return out[i].Inclusive > out[j].Inclusive
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// BuildCallTree rolls weighted stacks into a deterministic tree.
func BuildCallTree(samples []StackSample) CallTreeNode {
	root := treeNode{name: "root", children: make(map[string]*treeNode)}
	for _, sample := range samples {
		node := &root
		node.value += sample.Value
		for _, frame := range sample.Frames {
			child := node.children[frame]
			if child == nil {
				child = &treeNode{name: frame, children: make(map[string]*treeNode)}
				node.children[frame] = child
			}
			child.value += sample.Value
			node = child
		}
		node.self += sample.Value
	}
	return root.export()
}

type treeNode struct {
	name     string
	value    int64
	self     int64
	children map[string]*treeNode
}

func (n *treeNode) export() CallTreeNode {
	out := CallTreeNode{Name: n.name, Value: n.value, Self: n.self}
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		out.Children = append(out.Children, n.children[name].export())
	}
	return out
}

func splitFrames(stack string) []string {
	raw := strings.Split(stack, ";")
	frames := raw[:0]
	for _, frame := range raw {
		frame = strings.TrimSpace(frame)
		if frame != "" {
			frames = append(frames, frame)
		}
	}
	return frames
}
