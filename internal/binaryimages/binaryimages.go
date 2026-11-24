// Package binaryimages parses the "Binary Images:" section that macOS
// sample(1) and spindump print after their call trees, and maps a frame
// address to the image it falls in. It is pure text parsing, so it runs on
// any host.
package binaryimages

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Image is one loaded binary: its load address range, name, UUID and path.
// End is inclusive; zero means the report did not give an end address.
type Image struct {
	Name  string
	Path  string
	UUID  string
	Start uint64
	End   uint64
}

// Location identifies the code a frame address points at. Addresses and
// offsets are hex strings. Field names match crash inspect's frame_details.
type Location struct {
	Image       string `json:"image,omitempty"`
	ImagePath   string `json:"image_path,omitempty"`
	ImageUUID   string `json:"image_uuid,omitempty"`
	Arch        string `json:"arch,omitempty"`
	ImageBase   string `json:"image_base,omitempty"`
	ImageOffset string `json:"image_offset,omitempty"`
	Address     string `json:"address,omitempty"`
}

// IsZero reports whether l carries no information.
func (l Location) IsZero() bool { return l == Location{} }

// Table resolves addresses against a set of images from one process.
type Table struct {
	images []Image
	arch   string
}

var (
	// imageLine matches "0x1000 - 0x1fff  name (version) <UUID> /path". The
	// end address is "???" in some spindump entries; app-owned images in
	// sample carry a leading "+" on the name, and spindump marks kernel
	// images with a leading "*".
	imageLine = regexp.MustCompile(`^\s*\*?(0x[0-9a-fA-F]+)\s+-\s+(0x[0-9a-fA-F]+|\?\?\?)\s+(.*?)\s*$`)
	uuidField = regexp.MustCompile(`<([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})>`)
	frameAddr = regexp.MustCompile(`\[(0x[0-9a-fA-F]+)\]`)
)

// IsSectionHeader reports whether line starts a Binary Images section.
func IsSectionHeader(line string) bool {
	return strings.TrimSpace(line) == "Binary Images:"
}

// ParseLine parses one Binary Images entry.
func ParseLine(line string) (Image, bool) {
	m := imageLine.FindStringSubmatch(line)
	if m == nil {
		return Image{}, false
	}
	start, err := strconv.ParseUint(m[1][2:], 16, 64)
	if err != nil {
		return Image{}, false
	}
	var end uint64
	if m[2] != "???" {
		if end, err = strconv.ParseUint(m[2][2:], 16, 64); err != nil || end < start {
			return Image{}, false
		}
	}
	rest := m[3]
	im := Image{Start: start, End: end}
	if u := uuidField.FindStringSubmatchIndex(rest); u != nil {
		im.UUID = strings.ToUpper(rest[u[2]:u[3]])
		im.Path = sanitize(strings.TrimSpace(rest[u[1]:]))
		rest = rest[:u[0]]
	}
	im.Name = imageName(rest, im.Path)
	return im, true
}

// imageName prefers the path's base name, which is what frames print as
// "(in AppKit)" / "(AppKit + 12)"; the listed name is often a bundle ID.
func imageName(listed, p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Base(p)
	}
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(listed), "+"))
	if len(fields) == 0 || fields[0] == "???" {
		return ""
	}
	return sanitize(fields[0])
}

// NewTable builds a lookup table. arch is the process architecture as the
// report prints it ("ARM64", "X86-64", "arm64e"); it is normalized.
func NewTable(images []Image, arch string) *Table {
	sorted := append([]Image(nil), images...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	return &Table{images: sorted, arch: NormalizeArch(arch)}
}

// Len returns the number of images in the table.
func (t *Table) Len() int {
	if t == nil {
		return 0
	}
	return len(t.images)
}

// Lookup returns the location of addr, or false when no image contains it.
// An image without an end address extends to the next image's start.
func (t *Table) Lookup(addr uint64) (Location, bool) {
	if t == nil || len(t.images) == 0 {
		return Location{}, false
	}
	i := sort.Search(len(t.images), func(i int) bool { return t.images[i].Start > addr }) - 1
	if i < 0 {
		return Location{}, false
	}
	im := t.images[i]
	if im.End != 0 && addr > im.End {
		return Location{}, false
	}
	if im.End == 0 && i+1 < len(t.images) && addr >= t.images[i+1].Start {
		return Location{}, false
	}
	return Location{
		Image:       im.Name,
		ImagePath:   im.Path,
		ImageUUID:   im.UUID,
		Arch:        t.arch,
		ImageBase:   hex(im.Start),
		ImageOffset: hex(addr - im.Start),
		Address:     hex(addr),
	}, true
}

// LookupFrame resolves the "[0x...]" address in a call-tree frame's text.
func (t *Table) LookupFrame(text string) (Location, bool) {
	addr, ok := FrameAddress(text)
	if !ok {
		return Location{}, false
	}
	return t.Lookup(addr)
}

// FrameAddress extracts the last "[0x...]" address from a frame's text.
func FrameAddress(text string) (uint64, bool) {
	all := frameAddr.FindAllStringSubmatch(text, -1)
	if len(all) == 0 {
		return 0, false
	}
	v, err := strconv.ParseUint(all[len(all)-1][1][2:], 16, 64)
	return v, err == nil
}

// FromReport collects every Binary Images entry in a single-process report
// (sample output) and the "Code Type:" architecture from its header.
func FromReport(report string) *Table {
	var images []Image
	var arch string
	inSection := false
	for _, line := range strings.Split(report, "\n") {
		if arch == "" {
			if v, ok := HeaderArch(line); ok {
				arch = v
			}
		}
		if IsSectionHeader(line) {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if im, ok := ParseLine(line); ok {
			images = append(images, im)
		} else if strings.TrimSpace(line) != "" {
			inSection = false
		}
	}
	return NewTable(images, arch)
}

var archHeader = regexp.MustCompile(`^\s*(?:Code Type|Architecture):\s+(\S+)`)

// HeaderArch reads the architecture from a "Code Type:" (sample) or
// "Architecture:" (spindump) header line.
func HeaderArch(line string) (string, bool) {
	m := archHeader.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// NormalizeArch maps report spellings to the names crash reports and
// lldb use: "ARM64" -> "arm64", "X86-64" -> "x86_64".
func NormalizeArch(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "x86-64", "x86_64":
		return "x86_64"
	case "x86-64h", "x86_64h":
		return "x86_64h"
	case "", "???":
		return ""
	}
	return sanitize(s)
}

func hex(v uint64) string { return fmt.Sprintf("0x%x", v) }

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
