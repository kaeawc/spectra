// Package gclog parses JDK unified-logging garbage-collection output
// (`-Xlog:gc` and the `[gc]` summary lines of `-Xlog:gc*`) into per-pause
// records and an aggregate summary. It is the authoritative per-pause view the
// jstat-counter GC-pressure rule cannot provide.
//
// Scope: G1/Parallel/Serial "Pause ..." lines. ZGC/Shenandoah concurrent-cycle
// formats and the legacy pre-JDK9 -XX:+PrintGCDetails format are out of scope.
package gclog

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Pause is one stop-the-world GC pause parsed from a log line.
type Pause struct {
	ID       int     `json:"id"`
	Kind     string  `json:"kind"`  // Young, Full, Mixed, Remark, Cleanup, Other
	Cause    string  `json:"cause"` // e.g. "(G1 Evacuation Pause)", "(System.gc())"
	PauseMs  float64 `json:"pause_ms"`
	BeforeMB int64   `json:"before_mb"`
	AfterMB  int64   `json:"after_mb"`
	HeapMB   int64   `json:"heap_mb"`
}

// Summary aggregates all pauses in a log.
type Summary struct {
	Pauses             int            `json:"pauses"`
	TotalPauseMs       float64        `json:"total_pause_ms"`
	MaxPauseMs         float64        `json:"max_pause_ms"`
	AvgPauseMs         float64        `json:"avg_pause_ms"`
	FullGCCount        int            `json:"full_gc_count"`
	YoungGCCount       int            `json:"young_gc_count"`
	SystemGCCount      int            `json:"system_gc_count"`
	EvacuationFailures int            `json:"evacuation_failures"`
	Causes             map[string]int `json:"causes"`
	LongestPause       *Pause         `json:"longest_pause,omitempty"`
}

var (
	// idKindRe captures the GC id and pause kind: "GC(12) Pause Young".
	idKindRe = regexp.MustCompile(`GC\((\d+)\)\s+Pause\s+([A-Za-z]+)`)
	// durationRe captures a duration in ms; the LAST match on a line is the
	// total pause (sub-phase times, if any, precede it).
	durationRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)ms`)
	// heapRe captures "25M->5M(256M)" with K/M/G/B units.
	heapRe = regexp.MustCompile(`(\d+)([BKMG])->(\d+)([BKMG])\((\d+)([BKMG])\)`)
)

// ParseFile parses the GC log at path.
func ParseFile(path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	return Parse(f), nil
}

// Parse reads unified GC-log text and returns an aggregate Summary. It never
// errors; unrecognized lines are ignored.
func Parse(r io.Reader) Summary {
	s := Summary{Causes: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		p, ok := parsePause(line)
		if !ok {
			continue
		}
		accumulate(&s, p, line)
	}
	if s.Pauses > 0 {
		s.AvgPauseMs = s.TotalPauseMs / float64(s.Pauses)
	}
	return s
}

func accumulate(s *Summary, p Pause, raw string) {
	s.Pauses++
	s.TotalPauseMs += p.PauseMs
	if p.PauseMs > s.MaxPauseMs {
		s.MaxPauseMs = p.PauseMs
		lp := p
		s.LongestPause = &lp
	}
	switch p.Kind {
	case "Full":
		s.FullGCCount++
	case "Young", "Mixed":
		s.YoungGCCount++
	}
	if p.Cause != "" {
		s.Causes[p.Cause]++
	}
	if strings.Contains(p.Cause, "System.gc") {
		s.SystemGCCount++
	}
	if strings.Contains(raw, "Evacuation Failure") || strings.Contains(raw, "to-space exhausted") {
		s.EvacuationFailures++
	}
}

func parsePause(line string) (Pause, bool) {
	m := idKindRe.FindStringSubmatchIndex(line)
	if m == nil {
		return Pause{}, false
	}
	id, _ := strconv.Atoi(line[m[2]:m[3]])
	kindWord := line[m[4]:m[5]]

	dur := durationRe.FindAllStringSubmatch(line, -1)
	if len(dur) == 0 {
		return Pause{}, false // a "Pause" line with no duration isn't a completed pause
	}
	pauseMs, err := strconv.ParseFloat(dur[len(dur)-1][1], 64)
	if err != nil {
		return Pause{}, false
	}

	p := Pause{ID: id, Kind: normalizeKind(kindWord), PauseMs: pauseMs}

	heapStart := len(line)
	if h := heapRe.FindStringSubmatchIndex(line); h != nil {
		heapStart = h[0]
		p.BeforeMB = toMB(line[h[2]:h[3]], line[h[4]:h[5]])
		p.AfterMB = toMB(line[h[6]:h[7]], line[h[8]:h[9]])
		p.HeapMB = toMB(line[h[10]:h[11]], line[h[12]:h[13]])
	}
	// Cause is the text between the kind word and the heap/duration region.
	if m[5] < heapStart {
		p.Cause = strings.TrimSpace(line[m[5]:heapStart])
	}
	return p, true
}

func normalizeKind(word string) string {
	switch word {
	case "Young", "Full", "Mixed", "Remark", "Cleanup":
		return word
	default:
		return "Other"
	}
}

func toMB(numStr, unit string) int64 {
	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0
	}
	switch unit {
	case "B":
		return n / (1024 * 1024)
	case "K":
		return n / 1024
	case "M":
		return n
	case "G":
		return n * 1024
	default:
		return n
	}
}
