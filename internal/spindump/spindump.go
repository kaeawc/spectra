// Package spindump captures and summarizes macOS spindump reports — per-thread
// call trees with per-frame sample counts. The raw report is hundreds of lines
// of indented text; this package parses it into the heaviest symbols per
// process so a wedged or busy process can be triaged at a glance.
package spindump

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Frame is one symbol and how many samples landed on it.
type Frame struct {
	Symbol  string `json:"symbol"`
	Samples int    `json:"samples"`
}

// ProcessReport summarizes one process in a spindump report.
type ProcessReport struct {
	Name     string  `json:"name"`
	PID      int     `json:"pid"`
	Heaviest []Frame `json:"heaviest,omitempty"`
}

// Report is the parsed summary of a whole spindump report.
type Report struct {
	Duration  string          `json:"duration,omitempty"`
	Processes []ProcessReport `json:"processes"`
}

// defaultTopFrames is how many hot symbols to keep per process.
const defaultTopFrames = 5

var (
	processHeader = regexp.MustCompile(`^Process:\s+(.+?)\s+\[(\d+)\]`)
	durationLine  = regexp.MustCompile(`^Duration:\s+(.+)$`)
	// frameLine matches an indented "  <samples>  <rest>" call-tree line.
	frameLine = regexp.MustCompile(`^(\s+)(\d+)\s+(.*\S)\s*$`)
	// offsetSuffix matches a trailing " + 1234" byte offset on a symbol.
	offsetSuffix = regexp.MustCompile(`\s+\+\s+\d+$`)
)

// Parse turns a raw spindump report into a summary.
func Parse(report string) Report {
	var rep Report
	var cur *ProcessReport
	// samplesBySymbol accumulates the max sample count seen per symbol for the
	// current process.
	var samplesBySymbol map[string]int

	flush := func() {
		if cur != nil {
			cur.Heaviest = topFrames(samplesBySymbol, defaultTopFrames)
			rep.Processes = append(rep.Processes, *cur)
		}
	}

	for _, line := range strings.Split(report, "\n") {
		if m := durationLine.FindStringSubmatch(line); m != nil && rep.Duration == "" {
			rep.Duration = strings.TrimSpace(m[1])
			continue
		}
		if m := processHeader.FindStringSubmatch(line); m != nil {
			flush()
			pid, _ := strconv.Atoi(m[2])
			cur = &ProcessReport{Name: strings.TrimSpace(m[1]), PID: pid}
			samplesBySymbol = map[string]int{}
			continue
		}
		if cur == nil {
			continue
		}
		if m := frameLine.FindStringSubmatch(line); m != nil {
			samples, _ := strconv.Atoi(m[2])
			sym := frameSymbol(m[3])
			if sym == "" || isThreadHeader(m[3]) {
				continue
			}
			if samples > samplesBySymbol[sym] {
				samplesBySymbol[sym] = samples
			}
		}
	}
	flush()
	return rep
}

// frameSymbol extracts the symbol name from a call-tree frame's text, dropping
// the "(Image + off)" qualifier, the "[0x...]" address, and a trailing byte
// offset.
func frameSymbol(rest string) string {
	if i := strings.Index(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.Index(rest, " ["); i >= 0 {
		rest = rest[:i]
	}
	rest = offsetSuffix.ReplaceAllString(rest, "")
	return strings.TrimSpace(rest)
}

// isThreadHeader reports whether a frame's text is actually a thread or
// dispatch-queue header rather than a real stack frame.
func isThreadHeader(rest string) bool {
	return strings.Contains(rest, "Thread_") ||
		strings.Contains(rest, "DispatchQueue") ||
		strings.HasPrefix(rest, "Thread ")
}

func topFrames(bySymbol map[string]int, n int) []Frame {
	frames := make([]Frame, 0, len(bySymbol))
	for sym, samples := range bySymbol {
		frames = append(frames, Frame{Symbol: sym, Samples: samples})
	}
	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].Samples != frames[j].Samples {
			return frames[i].Samples > frames[j].Samples
		}
		return frames[i].Symbol < frames[j].Symbol
	})
	if len(frames) > n {
		frames = frames[:n]
	}
	return frames
}
