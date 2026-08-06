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

	"github.com/kaeawc/spectra/internal/threadinspect"
)

// ErrLegacyFormat is returned by Parse for pre-2020 plain-text .crash/.spin
// reports, which are not JSON. Callers can detect it with errors.Is.
var ErrLegacyFormat = errors.New("legacy plain-text crash report (not .ips JSON)")

// Report is a decoded crash report.
type Report struct {
	Process         string   `json:"process"`
	Version         string   `json:"version,omitempty"`
	BundleID        string   `json:"bundle_id,omitempty"`
	Path            string   `json:"path,omitempty"`
	OSVersion       string   `json:"os_version,omitempty"`
	Time            string   `json:"time,omitempty"`
	IncidentID      string   `json:"incident_id,omitempty"`
	PID             int      `json:"pid,omitempty"`
	Kind            string   `json:"kind"`
	BugType         string   `json:"bug_type,omitempty"`
	Exception       string   `json:"exception,omitempty"`
	ExceptionDetail string   `json:"exception_detail,omitempty"`
	Signal          string   `json:"signal,omitempty"`
	Codes           string   `json:"codes,omitempty"`
	Termination     string   `json:"termination,omitempty"`
	FaultingThread  int      `json:"faulting_thread"`
	Threads         []Thread `json:"threads"`
}

// Thread is one thread's decoded frames.
type Thread struct {
	Index     int      `json:"index"`
	Name      string   `json:"name,omitempty"`
	Queue     string   `json:"queue,omitempty"`
	Triggered bool     `json:"triggered,omitempty"`
	Frames    []string `json:"frames"`
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
	Name string `json:"name"`
	Path string `json:"path"`
	Base int64  `json:"base"`
	UUID string `json:"uuid"`
	Arch string `json:"arch"`
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
	for i, t := range b.Threads {
		th := Thread{
			Index:     i,
			Name:      t.Name,
			Queue:     t.Queue,
			Triggered: t.Triggered || i == b.FaultingThread,
		}
		for _, f := range t.Frames {
			th.Frames = append(th.Frames, formatFrame(f, b.UsedImages))
		}
		r.Threads = append(r.Threads, th)
	}
	return r
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
