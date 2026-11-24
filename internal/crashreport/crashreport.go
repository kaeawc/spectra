// Package crashreport parses modern macOS .ips crash reports into a structured,
// decoded form. An .ips file is a one-line JSON header followed by a JSON body;
// this package reads both, decodes the exception/termination reason and the
// numeric bug_type, resolves stack frames against the report's image list, and
// normalizes the threads into a threadinspect.Snapshot so the existing
// summarize/filter/diff machinery is reused.
package crashreport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/threadinspect"
)

// ErrLegacyFormat is returned by Parse for pre-2020 plain-text .crash/.spin
// reports, which are not JSON. Callers can detect it with errors.Is.
var ErrLegacyFormat = errors.New("legacy plain-text crash report (not .ips JSON)")

// Report is a decoded crash report.
type Report struct {
	Process         string        `json:"process"`
	Version         string        `json:"version,omitempty"`
	BundleID        string        `json:"bundle_id,omitempty"`
	Path            string        `json:"path,omitempty"`
	OSVersion       string        `json:"os_version,omitempty"`
	Time            string        `json:"time,omitempty"`
	IncidentID      string        `json:"incident_id,omitempty"`
	PID             int           `json:"pid,omitempty"`
	Kind            string        `json:"kind"`
	BugType         string        `json:"bug_type,omitempty"`
	Exception       string        `json:"exception,omitempty"`
	ExceptionDetail string        `json:"exception_detail,omitempty"`
	Signal          string        `json:"signal,omitempty"`
	Codes           string        `json:"codes,omitempty"`
	Termination     string        `json:"termination,omitempty"`
	Resource        *ResourceKill `json:"resource,omitempty"`
	FaultingThread  int           `json:"faulting_thread"`
	Threads         []Thread      `json:"threads"`
	Images          []Image       `json:"images,omitempty"`

	// SourceLinesUnsupported is set by ResolveSourceLines off macOS.
	SourceLinesUnsupported *hostos.UnsupportedReport `json:"source_lines_unsupported,omitempty"`
}

// Image is one binary from the report's usedImages list. Base is a hex
// string. OnDisk is filled by CheckImagesOnDisk and is empty until then.
type Image struct {
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	UUID   string `json:"uuid,omitempty"`
	Arch   string `json:"arch,omitempty"`
	Base   string `json:"base,omitempty"`
	OnDisk string `json:"image_on_disk,omitempty"`

	// inSharedCache records the report's own evidence (sharedCache range or
	// source "S") that the image was mapped from the dyld shared cache.
	inSharedCache bool
}

// FrameDetail is the structured form of one entry in Thread.Frames. Fields
// the report does not carry are omitted; addresses and offsets are hex.
type FrameDetail struct {
	Image        string `json:"image,omitempty"`
	ImagePath    string `json:"image_path,omitempty"`
	ImageUUID    string `json:"image_uuid,omitempty"`
	Arch         string `json:"arch,omitempty"`
	ImageBase    string `json:"image_base,omitempty"`
	ImageOffset  string `json:"image_offset"`
	Address      string `json:"address,omitempty"`
	Symbol       string `json:"symbol,omitempty"`
	SymbolOffset *int64 `json:"symbol_offset,omitempty"`

	// Set only by ResolveSourceLines.
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Column     int    `json:"column,omitempty"`
	ResolvedBy string `json:"resolved_by,omitempty"`
}

// ResourceKill describes an EXC_RESOURCE / watchdog-class termination: the OS
// killed the process for exceeding a CPU-time, wakeups, memory, or I/O ledger
// limit rather than for a fault. These read like mystery crashes but aren't.
type ResourceKill struct {
	Flavor      string `json:"flavor"`      // CPU, CPU_FATAL, WAKEUPS, IO, MEMORY, PORTS, THREADS, ...
	Explanation string `json:"explanation"` // plain-language cause
	Limit       string `json:"limit,omitempty"`
	Observed    string `json:"observed,omitempty"`
	Window      string `json:"window,omitempty"`
	Detail      string `json:"detail,omitempty"` // raw subtype/indicator text
}

// Thread is one thread's decoded frames.
type Thread struct {
	Index     int      `json:"index"`
	Name      string   `json:"name,omitempty"`
	Queue     string   `json:"queue,omitempty"`
	Triggered bool     `json:"triggered,omitempty"`
	Frames    []string `json:"frames"`
	// FrameDetails is parallel to Frames: FrameDetails[i] describes Frames[i].
	FrameDetails []FrameDetail `json:"frame_details,omitempty"`
}

// --- raw .ips shapes (best-effort; any field may be absent) ---

type ipsHeader struct {
	AppName    string `json:"app_name"`
	AppVersion string `json:"app_version"`
	BundleID   string `json:"bundleID"`
	Timestamp  string `json:"timestamp"`
	OSVersion  string `json:"os_version"`
	BugType    string `json:"bug_type"`
	IncidentID string `json:"incident_id"`
	Name       string `json:"name"`
}

type ipsBody struct {
	ProcName       string       `json:"procName"`
	ProcPath       string       `json:"procPath"`
	PID            int          `json:"pid"`
	Exception      ipsException `json:"exception"`
	Termination    ipsTerm      `json:"termination"`
	FaultingThread int          `json:"faultingThread"`
	Threads        []ipsThread  `json:"threads"`
	UsedImages     []ipsImage   `json:"usedImages"`
	SharedCache    ipsCache     `json:"sharedCache"`
}

type ipsCache struct {
	Base uint64 `json:"base"`
	Size uint64 `json:"size"`
}

type ipsException struct {
	Type    string `json:"type"`
	Signal  string `json:"signal"`
	Codes   string `json:"codes"`
	Subtype string `json:"subtype"`
}

type ipsTerm struct {
	Namespace string `json:"namespace"`
	Indicator string `json:"indicator"`
	Code      int    `json:"code"`
	ByProc    string `json:"byProc"`
	ByPid     int    `json:"byPid"`
}

type ipsThread struct {
	ID        int64      `json:"id"`
	Queue     string     `json:"queue"`
	Name      string     `json:"name"`
	Triggered bool       `json:"triggered"`
	Frames    []ipsFrame `json:"frames"`
}

type ipsFrame struct {
	ImageIndex     int    `json:"imageIndex"`
	ImageOffset    int64  `json:"imageOffset"`
	Symbol         string `json:"symbol"`
	SymbolLocation int64  `json:"symbolLocation"`
}

type ipsImage struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Base   uint64 `json:"base"`
	UUID   string `json:"uuid"`
	Arch   string `json:"arch"`
	Source string `json:"source"`
}

// Parse decodes a modern .ips crash report. It returns ErrLegacyFormat for
// pre-2020 plain-text reports.
func Parse(data []byte) (*Report, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("empty crash report")
	}
	if trimmed[0] != '{' {
		return nil, ErrLegacyFormat
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return nil, errors.New("malformed .ips: header line has no body")
	}
	var h ipsHeader
	if err := json.Unmarshal(bytes.TrimSpace(data[:nl]), &h); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	var b ipsBody
	if err := json.Unmarshal(data[nl+1:], &b); err != nil {
		return nil, fmt.Errorf("parse body: %w", err)
	}
	return build(h, b), nil
}

func build(h ipsHeader, b ipsBody) *Report {
	process := firstNonEmpty(b.ProcName, h.AppName, h.Name)
	r := &Report{
		Process:        process,
		Version:        h.AppVersion,
		BundleID:       h.BundleID,
		Path:           b.ProcPath,
		OSVersion:      h.OSVersion,
		Time:           h.Timestamp,
		IncidentID:     h.IncidentID,
		PID:            b.PID,
		BugType:        h.BugType,
		Kind:           crashKind(h.BugType),
		Signal:         b.Exception.Signal,
		Codes:          b.Exception.Codes,
		Termination:    b.Termination.Indicator,
		FaultingThread: b.FaultingThread,
	}
	if b.Exception.Type != "" {
		r.Exception = b.Exception.Type
		if b.Exception.Signal != "" {
			r.Exception = fmt.Sprintf("%s (%s)", b.Exception.Type, b.Exception.Signal)
		}
		r.ExceptionDetail = explainException(b.Exception.Type)
	}
	if b.Exception.Type == "EXC_RESOURCE" {
		r.Resource = decodeResource(b.Exception, b.Termination)
	}
	for i, t := range b.Threads {
		th := Thread{
			Index:     i,
			Name:      t.Name,
			Queue:     t.Queue,
			Triggered: t.Triggered || i == b.FaultingThread,
		}
		for _, f := range t.Frames {
			th.Frames = append(th.Frames, formatFrame(f, b.UsedImages))
			th.FrameDetails = append(th.FrameDetails, frameDetail(f, b.UsedImages))
		}
		r.Threads = append(r.Threads, th)
	}
	r.Images = buildImages(b.UsedImages, b.SharedCache)
	return r
}

func buildImages(images []ipsImage, cache ipsCache) []Image {
	var out []Image
	for _, im := range images {
		if im.Name == "" && im.Path == "" {
			continue
		}
		out = append(out, Image{
			Name:          im.Name,
			Path:          im.Path,
			UUID:          reportUUID(im.UUID),
			Arch:          im.Arch,
			Base:          hexAddr(im.Base),
			inSharedCache: im.Source == "S" || cache.contains(im.Base),
		})
	}
	return out
}

func (c ipsCache) contains(addr uint64) bool {
	return c.Size > 0 && addr >= c.Base && addr-c.Base < c.Size
}

// frameDetail mirrors formatFrame without flattening: it keeps the image
// identity and the offsets so a consumer can locate the exact instruction.
func frameDetail(f ipsFrame, images []ipsImage) FrameDetail {
	d := FrameDetail{ImageOffset: fmt.Sprintf("0x%x", f.ImageOffset), Symbol: f.Symbol}
	if f.Symbol != "" {
		loc := f.SymbolLocation
		d.SymbolOffset = &loc
	}
	if f.ImageIndex < 0 || f.ImageIndex >= len(images) {
		return d
	}
	im := images[f.ImageIndex]
	d.Image = im.Name
	d.ImagePath = im.Path
	d.ImageUUID = reportUUID(im.UUID)
	d.Arch = im.Arch
	d.ImageBase = hexAddr(im.Base)
	if im.Base != 0 && f.ImageOffset >= 0 {
		d.Address = hexAddr(im.Base + uint64(f.ImageOffset))
	}
	return d
}

func hexAddr(v uint64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("0x%x", v)
}

// reportUUID drops the all-zero placeholder UUID that reports use for
// images they could not identify.
func reportUUID(u string) string {
	if strings.Trim(u, "0-") == "" {
		return ""
	}
	return u
}

// Snapshot normalizes the report's threads into a threadinspect.Snapshot so the
// shared summarize/filter/diff helpers apply to native crashes.
func (r *Report) Snapshot() threadinspect.Snapshot {
	s := threadinspect.Snapshot{Runtime: threadinspect.RuntimeNative, PID: r.PID}
	for _, t := range r.Threads {
		name := t.Name
		if name == "" {
			name = fmt.Sprintf("Thread %d", t.Index)
		}
		if t.Triggered {
			name += " (faulting)"
		}
		th := threadinspect.Thread{
			Name:      name,
			NativeID:  fmt.Sprintf("%d", t.Index),
			Detail:    t.Queue,
			Stack:     t.Frames,
			RuntimeID: t.Queue,
		}
		if len(t.Frames) > 0 {
			th.TopFrame = t.Frames[0]
		}
		s.Threads = append(s.Threads, th)
	}
	return s
}

func formatFrame(f ipsFrame, images []ipsImage) string {
	img := "???"
	if f.ImageIndex >= 0 && f.ImageIndex < len(images) && images[f.ImageIndex].Name != "" {
		img = images[f.ImageIndex].Name
	}
	if f.Symbol != "" {
		return fmt.Sprintf("%s`%s + %d", img, f.Symbol, f.SymbolLocation)
	}
	return fmt.Sprintf("%s + 0x%x", img, f.ImageOffset)
}

// crashKind maps the numeric bug_type to a human kind. Modern reports use
// numeric codes rather than the old crash/hang strings. Best-effort: unknown
// codes fall back to a generic label that still names the raw code.
func crashKind(bugType string) string {
	switch bugType {
	case "309", "385", "108", "109":
		return "crash"
	case "288":
		return "hang"
	case "":
		return "crash report"
	default:
		return "crash report (bug_type " + bugType + ")"
	}
}

var (
	reResourceWindow   = regexp.MustCompile(`(\d+)\s*s\b`)
	reResourceLimit    = regexp.MustCompile(`(?i)limit[:\s]*([0-9]+%?)`)
	reResourceObserved = regexp.MustCompile(`(?i)(?:was|observed|used)[:\s]*([0-9]+%?)`)
)

// decodeResource turns an EXC_RESOURCE exception into a plain-language kill
// description. It reads the flavor from the exception subtype and pulls any
// limit/observed/window numbers out of the human strings best-effort, so it is
// robust to per-flavor schema drift across macOS releases.
func decodeResource(exc ipsException, term ipsTerm) *ResourceKill {
	flavorSrc := exc.Subtype
	if strings.TrimSpace(flavorSrc) == "" {
		flavorSrc = term.Indicator
	}
	flavor := resourceFlavor(flavorSrc)
	detail := strings.TrimSpace(exc.Subtype)
	if detail == "" {
		detail = strings.TrimSpace(term.Indicator)
	}
	rk := &ResourceKill{
		Flavor:      flavor,
		Explanation: explainResource(flavor),
		Detail:      detail,
	}
	blob := exc.Subtype + " " + term.Indicator + " " + exc.Codes
	if m := reResourceWindow.FindStringSubmatch(blob); m != nil {
		rk.Window = m[1] + "s"
	}
	if m := reResourceLimit.FindStringSubmatch(blob); m != nil {
		rk.Limit = m[1]
	}
	if m := reResourceObserved.FindStringSubmatch(blob); m != nil {
		rk.Observed = m[1]
	}
	return rk
}

// resourceFlavor extracts the leading resource-type word from an EXC_RESOURCE
// subtype (e.g. "WAKEUPS (Value=...)" -> "WAKEUPS").
func resourceFlavor(subtype string) string {
	f := strings.ToUpper(strings.TrimSpace(subtype))
	if f == "" {
		return "UNKNOWN"
	}
	if i := strings.IndexAny(f, " ("); i > 0 {
		f = f[:i]
	}
	return f
}

func explainResource(flavor string) string {
	switch flavor {
	case "CPU", "CPU_FATAL":
		return "sustained CPU use tripped the OS CPU-time watchdog over a rolling window (a busy loop or runaway thread), not a fault"
	case "WAKEUPS":
		return "excessive timer/interrupt wakeups tripped the power watchdog (typically a tight polling loop)"
	case "IO":
		return "sustained disk I/O exceeded the process I/O ledger limit"
	case "MEMORY":
		return "resident memory crossed a high-watermark limit"
	case "PORTS":
		return "the process exhausted its Mach port allocation"
	case "THREADS":
		return "the process exceeded its thread-count limit"
	default:
		return "a resource ledger limit was exceeded"
	}
}

func explainException(excType string) string {
	switch excType {
	case "EXC_BAD_ACCESS":
		return "invalid memory access (segmentation fault / bad pointer)"
	case "EXC_BAD_INSTRUCTION":
		return "illegal instruction (often a Swift trap, assertion, or precondition failure)"
	case "EXC_CRASH":
		return "abnormal exit (abort() or an uncaught Objective-C/C++ exception)"
	case "EXC_BREAKPOINT":
		return "trap/breakpoint (Swift fatalError/precondition, or a debugger trap)"
	case "EXC_ARITHMETIC":
		return "arithmetic error (e.g. integer divide by zero)"
	case "EXC_GUARD":
		return "guarded-resource violation (e.g. illegal use of a guarded file descriptor)"
	case "EXC_RESOURCE":
		return "resource limit exceeded (CPU time, memory, or wakeups)"
	default:
		return ""
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
