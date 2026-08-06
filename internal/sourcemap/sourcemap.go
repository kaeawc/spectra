// Package sourcemap decodes Source Map v3 files and resolves a generated
// (minified) position back to its original source location. It implements the
// base64-VLQ mappings format with the standard library only — no third-party
// dependency — so minified JS stack frames can be symbolicated.
package sourcemap

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// Position is a resolved original source location.
type Position struct {
	Source string `json:"source"`
	Line   int    `json:"line"`   // 1-based original line
	Column int    `json:"column"` // 0-based original column
	Name   string `json:"name,omitempty"`
}

// SourceMap is a parsed, queryable source map.
type SourceMap struct {
	sources []string
	names   []string
	root    string
	lines   [][]segment // per 0-based generated line, sorted by generated column
}

type segment struct {
	genCol    int
	sourceIdx int
	srcLine   int
	srcCol    int
	nameIdx   int
	hasSource bool
	hasName   bool
}

type rawMap struct {
	Version    int      `json:"version"`
	Sources    []string `json:"sources"`
	Names      []string `json:"names"`
	Mappings   string   `json:"mappings"`
	SourceRoot string   `json:"sourceRoot"`
}

// Parse decodes a Source Map v3 document.
func Parse(data []byte) (*SourceMap, error) {
	var r rawMap
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("sourcemap: %w", err)
	}
	if r.Version != 0 && r.Version != 3 {
		return nil, fmt.Errorf("sourcemap: unsupported version %d", r.Version)
	}
	sm := &SourceMap{sources: r.Sources, names: r.Names, root: r.SourceRoot}
	if err := sm.decodeMappings(r.Mappings); err != nil {
		return nil, err
	}
	return sm, nil
}

// accum holds the cross-line running values the VLQ deltas accumulate against.
type accum struct{ sourceIdx, srcLine, srcCol, nameIdx int }

func (sm *SourceMap) decodeMappings(mappings string) error {
	lines := strings.Split(mappings, ";")
	sm.lines = make([][]segment, len(lines))
	var a accum
	for li, line := range lines {
		if line == "" {
			continue
		}
		genCol := 0
		for _, raw := range strings.Split(line, ",") {
			if raw == "" {
				continue
			}
			vals, err := decodeVLQ(raw)
			if err != nil {
				return err
			}
			seg, err := buildSegment(vals, &genCol, &a)
			if err != nil {
				return err
			}
			sm.lines[li] = append(sm.lines[li], seg)
		}
		sort.Slice(sm.lines[li], func(i, j int) bool { return sm.lines[li][i].genCol < sm.lines[li][j].genCol })
	}
	return nil
}

func buildSegment(vals []int, genCol *int, a *accum) (segment, error) {
	if len(vals) != 1 && len(vals) != 4 && len(vals) != 5 {
		return segment{}, fmt.Errorf("sourcemap: segment has %d fields, want 1, 4, or 5", len(vals))
	}
	*genCol += vals[0]
	s := segment{genCol: *genCol, nameIdx: -1}
	if len(vals) >= 4 {
		a.sourceIdx += vals[1]
		a.srcLine += vals[2]
		a.srcCol += vals[3]
		s.sourceIdx, s.srcLine, s.srcCol, s.hasSource = a.sourceIdx, a.srcLine, a.srcCol, true
	}
	if len(vals) == 5 {
		a.nameIdx += vals[4]
		s.nameIdx, s.hasName = a.nameIdx, true
	}
	return s, nil
}

// decodeVLQ decodes one comma-free mapping segment into its integer fields.
func decodeVLQ(seg string) ([]int, error) {
	var out []int
	var shift uint
	var value int
	for i := 0; i < len(seg); i++ {
		d := strings.IndexByte(base64Alphabet, seg[i])
		if d < 0 {
			return nil, fmt.Errorf("sourcemap: invalid base64 char %q", seg[i])
		}
		digit := d & 0x1f
		value += digit << shift
		if d&0x20 != 0 { // continuation bit set
			shift += 5
			continue
		}
		neg := value&1 != 0
		v := value >> 1
		if neg {
			v = -v
		}
		out = append(out, v)
		value, shift = 0, 0
	}
	if shift != 0 {
		return nil, errors.New("sourcemap: truncated VLQ sequence")
	}
	return out, nil
}

// Lookup resolves a generated position (1-based line, 0-based column) to its
// original source location. It returns false when no mapping covers it.
func (sm *SourceMap) Lookup(genLine, genCol int) (Position, bool) {
	li := genLine - 1
	if li < 0 || li >= len(sm.lines) {
		return Position{}, false
	}
	segs := sm.lines[li]
	if len(segs) == 0 {
		return Position{}, false
	}
	// greatest segment whose generated column is <= genCol
	idx := sort.Search(len(segs), func(i int) bool { return segs[i].genCol > genCol }) - 1
	if idx < 0 {
		return Position{}, false
	}
	s := segs[idx]
	if !s.hasSource {
		return Position{}, false
	}
	pos := Position{Line: s.srcLine + 1, Column: s.srcCol}
	if s.sourceIdx >= 0 && s.sourceIdx < len(sm.sources) {
		pos.Source = sm.root + sm.sources[s.sourceIdx]
	}
	if s.hasName && s.nameIdx >= 0 && s.nameIdx < len(sm.names) {
		pos.Name = sm.names[s.nameIdx]
	}
	return pos, true
}
