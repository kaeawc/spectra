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

	"github.com/kaeawc/spectra/internal/binaryimages"
)

// Frame is one symbol and how many samples landed on it. When the process's
// Binary Images section places the frame's address in a known image, the
// embedded Location identifies the image and the offset into it.
type Frame struct {
	Symbol  string `json:"symbol"`
	Samples int    `json:"samples"`
	binaryimages.Location
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
	p := parser{}
	for _, line := range strings.Split(report, "\n") {
		p.line(line)
	}
	p.flush()
	return p.rep
}

// parser holds the state of one pass over a report. Images and the
// architecture are per process; an Architecture line before the first
// process is the fallback for processes that do not print their own.
type parser struct {
	rep        Report
	cur        *ProcessReport
	globalArch string
	arch       string
	inImages   bool
	images     []binaryimages.Image
	// samplesBySymbol accumulates the max sample count seen per symbol for
	// the current process; addrBySymbol is the frame address on that line.
	samplesBySymbol map[string]int
	addrBySymbol    map[string]uint64
}

func (p *parser) line(line string) {
	if m := durationLine.FindStringSubmatch(line); m != nil && p.rep.Duration == "" {
		p.rep.Duration = strings.TrimSpace(m[1])
		return
	}
	if m := processHeader.FindStringSubmatch(line); m != nil {
		p.flush()
		pid, _ := strconv.Atoi(m[2])
		p.cur = &ProcessReport{Name: sanitize(strings.TrimSpace(m[1])), PID: pid}
		p.samplesBySymbol = map[string]int{}
		p.addrBySymbol = map[string]uint64{}
		p.arch, p.images, p.inImages = "", nil, false
		return
	}
	if arch, ok := binaryimages.HeaderArch(line); ok {
		if p.cur == nil {
			p.globalArch = arch
		} else if p.arch == "" {
			p.arch = arch
		}
		return
	}
	if p.cur == nil || p.imageLine(line) {
		return
	}
	if m := frameLine.FindStringSubmatch(line); m != nil {
		p.frame(m)
	}
}

// imageLine consumes Binary Images header and entry lines.
func (p *parser) imageLine(line string) bool {
	if binaryimages.IsSectionHeader(line) {
		p.inImages = true
		return true
	}
	if !p.inImages {
		return false
	}
	if im, ok := binaryimages.ParseLine(line); ok {
		p.images = append(p.images, im)
		return true
	}
	if strings.TrimSpace(line) != "" {
		p.inImages = false
	}
	return false
}

func (p *parser) frame(m []string) {
	samples, _ := strconv.Atoi(m[2])
	sym := frameSymbol(m[3])
	if sym == "" || isThreadHeader(m[3]) {
		return
	}
	if samples <= p.samplesBySymbol[sym] {
		return
	}
	p.samplesBySymbol[sym] = samples
	if addr, ok := binaryimages.FrameAddress(m[3]); ok {
		p.addrBySymbol[sym] = addr
	} else {
		delete(p.addrBySymbol, sym)
	}
}

func (p *parser) flush() {
	if p.cur == nil {
		return
	}
	p.cur.Heaviest = topFrames(p.samplesBySymbol, defaultTopFrames)
	if len(p.images) > 0 {
		arch := p.arch
		if arch == "" {
			arch = p.globalArch
		}
		table := binaryimages.NewTable(p.images, arch)
		for i, f := range p.cur.Heaviest {
			if addr, ok := p.addrBySymbol[f.Symbol]; ok {
				p.cur.Heaviest[i].Location, _ = table.Lookup(addr)
			}
		}
	}
	p.rep.Processes = append(p.rep.Processes, *p.cur)
	p.cur = nil
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
	return sanitize(strings.TrimSpace(rest))
}

// sanitize removes terminal control bytes from text taken from a report, so an
// untrusted report cannot inject escape sequences into the terminal or JSON.
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
