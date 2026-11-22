package detect

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
)

// Streaming marker search for pythonScan. Each candidate file (anything under
// Contents/MacOS, plus *.so for the Nuitka check) is opened once and read in
// fixed-size chunks; every marker the detection passes need is collected in
// that single pass and cached by path.
const (
	pythonScanChunk     = 64 << 10
	pythonMaxTokenCarry = 64 << 10
	// pythonMinMarkerLen is the shortest marker any pass looks for ("python",
	// "nuitka"); regular files smaller than this are not opened.
	pythonMinMarkerLen = 6
	// pythonChunkOverlap is len("DYLD_INSERT_LIBRARIES=")-1, the longest
	// needle; it also covers "/opt/homebrew/" and the 9-byte raw forms of
	// "nuitka" that lowercase to it.
	pythonChunkOverlap = 21
	// pythonPrefixOverlap is len("/opt/homebrew/")-1.
	pythonPrefixOverlap = 13
)

var (
	pythonLauncherNeedles = [][]byte{[]byte("python"), []byte("Python.framework"), []byte("libpython")}
	pythonEnvNeedles      = [][]byte{[]byte("PYTHONPATH="), []byte("PYTHONUSERBASE="), []byte("DYLD_LIBRARY_PATH="), []byte("DYLD_INSERT_LIBRARIES=")}
	// Every externalPythonInterpreterRE match starts with one of these.
	pythonInterpreterPrefixes = [][]byte{[]byte("/usr/"), []byte("/opt/homebrew/"), []byte("/Users/")}
	pythonNeedle              = []byte("python")
	nuitkaNeedle              = []byte("nuitka")
	// Non-ASCII runes that unicode.ToLower maps into ASCII: U+0130 -> 'i',
	// U+212A -> 'k'. Only these can make bytes.ToLower produce "nuitka" from
	// a non-ASCII spelling.
	pythonLowerToASCII = [][]byte{[]byte("İ"), []byte("K")}
)

// pythonTokenDelim is the complement of [^[:space:]"'] in
// externalPythonInterpreterRE: no match can span one of these bytes.
var pythonTokenDelim = func() (t [256]bool) {
	for _, c := range []byte(" \t\n\v\f\r\"'") {
		t[c] = true
	}
	return t
}()

type pythonFileMarkers struct {
	launcherPython bool   // contains python / Python.framework / libpython
	envSensitive   bool   // contains one of pythonEnvNeedles
	nuitka         bool   // bytes.ToLower(content) contains "nuitka"
	interpreter    string // first externalPythonInterpreterRE match
}

func (s *pythonScan) fileMarkers(e pythonWalkEntry) pythonFileMarkers {
	if m, ok := s.markers[e.path]; ok {
		return m
	}
	m := s.scanFileMarkers(e)
	s.markers[e.path] = m
	return m
}

func (s *pythonScan) scanFileMarkers(e pythonWalkEntry) pythonFileMarkers {
	launcher := e.path == s.macos || strings.HasPrefix(e.path, s.macos+string(filepath.Separator))
	so := strings.HasSuffix(strings.ToLower(e.d.Name()), ".so")
	if !launcher && !so {
		return pythonFileMarkers{}
	}
	if e.d.Type().IsRegular() {
		if info, err := e.d.Info(); err == nil && info.Size() < pythonMinMarkerLen {
			return pythonFileMarkers{}
		}
	}
	f, err := s.fs.Open(e.path)
	if err != nil {
		return pythonFileMarkers{}
	}
	defer func() { _ = f.Close() }()
	ms := newPythonMarkerSearch(launcher, so, s.chunk, s.tokenCap, f)
	if len(s.buf) < ms.bufLen() {
		s.buf = make([]byte, ms.bufLen())
	}
	m, err := ms.run(f, s.buf[:ms.bufLen()])
	if err != nil {
		// Matches os.ReadFile failing: the old passes saw no content at all.
		return pythonFileMarkers{}
	}
	return m
}

// pythonMarkerSearch scans one file. Offsets named abs/cand/... are absolute
// file offsets; win is the current window (retained tail + new chunk) that
// starts at winStart.
type pythonMarkerSearch struct {
	launcher, so bool
	chunk        int
	tokenCap     int
	file         io.ReaderAt
	m            pythonFileMarkers
	interpDone   bool
	lower        []byte

	// Interpreter regex state. Matches never cross a token delimiter, and the
	// first interpreter prefix in a token dominates later ones, so only the
	// first candidate of the current token is tracked.
	candFrom  int64 // next offset to search for a candidate prefix
	cand      int64 // first candidate of the open token, or -1
	prefixPos [3]int
	// Long-token mode: the token from cand outgrew tokenCap, so the regex is
	// evaluated from offsets (match = cand .. last "python" + [0-9.]*).
	long       bool
	threshold  int64 // earliest offset a "python" may start at, -1 unknown
	usersCand  bool
	scanned    int64
	lastPy     int64
	digitEnd   int64
	digitsOpen bool
}

func newPythonMarkerSearch(launcher, so bool, chunk, tokenCap int, file io.ReaderAt) *pythonMarkerSearch {
	if chunk <= 0 {
		chunk = pythonScanChunk
	}
	if tokenCap <= 0 {
		tokenCap = pythonMaxTokenCarry
	}
	return &pythonMarkerSearch{launcher: launcher, so: so, chunk: chunk, tokenCap: tokenCap, file: file, cand: -1}
}

// bufLen fits the largest retained tail (an open token up to tokenCap, or the
// needle overlap) plus one chunk.
func (ms *pythonMarkerSearch) bufLen() int {
	return max(ms.tokenCap, pythonChunkOverlap) + ms.chunk
}

func (ms *pythonMarkerSearch) done() bool {
	launcherDone := !ms.launcher || (ms.m.launcherPython && ms.m.envSensitive && ms.interpDone)
	return launcherDone && (!ms.so || ms.m.nuitka)
}

func (ms *pythonMarkerSearch) run(r io.Reader, buf []byte) (pythonFileMarkers, error) {
	keep := 0
	var winStart int64
	for {
		n, err := io.ReadFull(r, buf[keep:keep+ms.chunk])
		final := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if err != nil && !final {
			return pythonFileMarkers{}, err
		}
		win := buf[:keep+n]
		if err := ms.process(win, winStart, final); err != nil {
			return pythonFileMarkers{}, err
		}
		if final || ms.done() {
			return ms.m, nil
		}
		from := max(len(win)-pythonChunkOverlap, 0)
		if ms.cand >= 0 && !ms.long {
			from = min(from, int(ms.cand-winStart))
		}
		keep = copy(buf, win[from:])
		winStart += int64(from)
	}
}

func (ms *pythonMarkerSearch) process(win []byte, winStart int64, final bool) error {
	if ms.so && !ms.m.nuitka {
		ms.m.nuitka = ms.containsNuitka(win)
	}
	if !ms.launcher {
		return nil
	}
	if !ms.m.launcherPython {
		ms.m.launcherPython = containsAnyNeedle(win, pythonLauncherNeedles)
	}
	if !ms.m.envSensitive {
		ms.m.envSensitive = containsAnyNeedle(win, pythonEnvNeedles)
	}
	if ms.interpDone {
		return nil
	}
	return ms.scanInterpreter(win, winStart, final)
}

func containsAnyNeedle(win []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(win, n) {
			return true
		}
	}
	return false
}

// containsNuitka reports whether bytes.ToLower(win) contains "nuitka" without
// allocating for the common case: ASCII folding into a reused buffer, and the
// exact Unicode lowering only when a rune that lowers into ASCII is present.
func (ms *pythonMarkerSearch) containsNuitka(win []byte) bool {
	if cap(ms.lower) < len(win) {
		ms.lower = make([]byte, len(win))
	}
	lower := ms.lower[:len(win)]
	for i, c := range win {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		lower[i] = c
	}
	if bytes.Contains(lower, nuitkaNeedle) {
		return true
	}
	if containsAnyNeedle(win, pythonLowerToASCII) {
		return bytes.Contains(bytes.ToLower(win), nuitkaNeedle)
	}
	return false
}

func (ms *pythonMarkerSearch) scanInterpreter(win []byte, winStart int64, final bool) error {
	ms.prefixPos = [3]int{-2, -2, -2}
	for !ms.interpDone {
		if ms.cand < 0 && !ms.findCandidate(win, winStart) {
			return nil
		}
		var more bool
		var err error
		if ms.long {
			more, err = ms.stepLong(win, winStart, final)
		} else {
			more = ms.stepShort(win, winStart, final)
		}
		if err != nil || !more {
			return err
		}
	}
	return nil
}

func (ms *pythonMarkerSearch) findCandidate(win []byte, winStart int64) bool {
	c := ms.nextCandidate(win, int(ms.candFrom-winStart))
	if c < 0 {
		ms.candFrom = max(ms.candFrom, winStart+int64(len(win))-pythonPrefixOverlap)
		return false
	}
	ms.cand, ms.long = winStart+int64(c), false
	return true
}

// stepShort evaluates the open token with the real regex once it is complete
// in the window. It reports whether the window has more to scan.
func (ms *pythonMarkerSearch) stepShort(win []byte, winStart int64, final bool) bool {
	c := int(ms.cand - winStart)
	d := indexPythonTokenDelim(win[c:])
	if d < 0 && !final {
		if len(win)-c > ms.tokenCap {
			ms.startLong(win[c:])
			return true
		}
		return false
	}
	tokEnd := len(win)
	if d >= 0 {
		tokEnd = c + d
	}
	tok := win[c:tokEnd]
	if bytes.Contains(tok, pythonNeedle) {
		if m := externalPythonInterpreterRE.FindSubmatch(tok); len(m) > 1 {
			ms.m.interpreter, ms.interpDone, ms.cand = string(m[1]), true, -1
			return false
		}
	}
	ms.cand, ms.candFrom = -1, winStart+int64(tokEnd)+1
	return d >= 0
}

// stepLong advances a long token; at its end the match, if any, is read back
// by offset so the token itself is never buffered.
func (ms *pythonMarkerSearch) stepLong(win []byte, winStart int64, final bool) (bool, error) {
	ended, tokEnd := ms.advanceLong(win, winStart, final)
	if !ended {
		return false, nil
	}
	if ms.threshold >= 0 && ms.lastPy >= ms.threshold {
		match := make([]byte, ms.digitEnd-ms.cand)
		if _, err := ms.file.ReadAt(match, ms.cand); err != nil {
			return false, err
		}
		ms.m.interpreter, ms.interpDone, ms.cand = string(match), true, -1
		return false, nil
	}
	ms.cand, ms.candFrom = -1, tokEnd+1
	return true, nil
}

// nextCandidate returns the window index of the first interpreter prefix at
// or after from, or -1. Per-prefix positions are cached for the window so a
// prefix absent from the window is searched for only once.
func (ms *pythonMarkerSearch) nextCandidate(win []byte, from int) int {
	best := -1
	if from >= len(win) {
		return best
	}
	for i, p := range pythonInterpreterPrefixes {
		pos := ms.prefixPos[i]
		if pos == -2 || (pos >= 0 && pos < from) {
			pos = -1
			if j := bytes.Index(win[from:], p); j >= 0 {
				pos = from + j
			}
			ms.prefixPos[i] = pos
		}
		if pos >= 0 && (best < 0 || pos < best) {
			best = pos
		}
	}
	return best
}

func (ms *pythonMarkerSearch) startLong(tok []byte) {
	ms.long, ms.scanned, ms.lastPy, ms.digitsOpen = true, ms.cand, -1, false
	ms.threshold, ms.usersCand = -1, false
	switch {
	case bytes.HasPrefix(tok, pythonInterpreterPrefixes[0]):
		ms.threshold = ms.cand + int64(len(pythonInterpreterPrefixes[0]))
	case bytes.HasPrefix(tok, pythonInterpreterPrefixes[1]):
		ms.threshold = ms.cand + int64(len(pythonInterpreterPrefixes[1]))
	default:
		// /Users/[^...]+/ needs a slash at least one byte after "/Users/".
		ms.usersCand = true
	}
}

// advanceLong consumes the open long token's bytes in win, reporting whether
// the token ended and where.
func (ms *pythonMarkerSearch) advanceLong(win []byte, winStart int64, final bool) (bool, int64) {
	winEnd := winStart + int64(len(win))
	tokEnd := winEnd
	ended := final
	if d := indexPythonTokenDelim(win[ms.scanned-winStart:]); d >= 0 {
		tokEnd, ended = ms.scanned+int64(d), true
	}
	if ms.usersCand && ms.threshold < 0 {
		if from := max(ms.cand+8, ms.scanned); from < tokEnd {
			if q := bytes.IndexByte(win[from-winStart:tokEnd-winStart], '/'); q >= 0 {
				ms.threshold = from + int64(q) + 1
			}
		}
	}
	pyFrom := max(ms.cand, ms.scanned-int64(len(pythonNeedle)-1), winStart)
	if p := lastIndexPython(win[pyFrom-winStart : tokEnd-winStart]); p >= 0 && pyFrom+int64(p) > ms.lastPy {
		ms.lastPy = pyFrom + int64(p)
		ms.digitEnd, ms.digitsOpen = ms.lastPy+int64(len(pythonNeedle)), true
	}
	for ms.digitsOpen && ms.digitEnd < tokEnd {
		c := win[ms.digitEnd-winStart]
		if c != '.' && (c < '0' || c > '9') {
			ms.digitsOpen = false
			break
		}
		ms.digitEnd++
	}
	ms.scanned = tokEnd
	return ended, tokEnd
}

// lastIndexPython guards the non-vectorized bytes.LastIndex with a
// vectorized bytes.Index so tokens without "python" stay cheap.
func lastIndexPython(b []byte) int {
	if !bytes.Contains(b, pythonNeedle) {
		return -1
	}
	return bytes.LastIndex(b, pythonNeedle)
}

func indexPythonTokenDelim(b []byte) int {
	for i, c := range b {
		if pythonTokenDelim[c] {
			return i
		}
	}
	return -1
}
