// Package hostos identifies the host operating system. It is the single
// source of truth Spectra consults whenever it must choose OS-specific
// behavior: which binary to invoke, which path to read, or whether a
// feature is supported on the host at all.
//
// Collectors do not read runtime.GOOS directly. They either accept an OS
// Kind option (whose zero value, Unknown, resolves to the host via
// Resolve) or call Current. Both routes go through the same overridable
// resolver, so a test can exercise another platform's code path on any
// host with SetForTest.
package hostos

import (
	"errors"
	"fmt"
	"runtime"
)

// Kind is a host operating-system family.
type Kind int

const (
	// Unknown is the zero value: OS not determined. As a collector
	// option it means "detect the host" (see Resolve).
	Unknown Kind = iota
	// Darwin is macOS.
	Darwin
	// Linux is any Linux distribution.
	Linux
)

// String returns the lowercase GOOS-style name ("darwin", "linux", or
// "unknown").
func (k Kind) String() string {
	switch k {
	case Darwin:
		return "darwin"
	case Linux:
		return "linux"
	default:
		return "unknown"
	}
}

// current resolves the host OS. It is a package var so SetForTest can
// override the detected OS; production always uses fromGOOS.
var current = fromGOOS

func fromGOOS() Kind {
	switch runtime.GOOS {
	case "darwin":
		return Darwin
	case "linux":
		return Linux
	default:
		return Unknown
	}
}

// Current returns the host operating system.
func Current() Kind { return current() }

// Resolve returns k unless it is Unknown, in which case it returns the
// host OS. Collectors take an OS Kind option whose zero value means
// "detect the host"; they pass it through Resolve to get a concrete Kind.
func Resolve(k Kind) Kind {
	if k == Unknown {
		return Current()
	}
	return k
}

// SetForTest overrides the OS reported by Current (and by Resolve for an
// Unknown input) for the duration of a test. It returns a function that
// restores the previous resolver; call it with defer.
func SetForTest(k Kind) func() {
	prev := current
	current = func() Kind { return k }
	return func() { current = prev }
}

// ErrUnsupported is the sentinel wrapped by every error Unsupported
// returns. Callers detect an unsupported-feature failure with
// errors.Is(err, hostos.ErrUnsupported).
var ErrUnsupported = errors.New("unsupported on host OS")

// UnsupportedError is the error Unsupported returns. errors.As recovers the
// feature and OS, for example to report them as structured output.
type UnsupportedError struct {
	Feature string
	OS      Kind
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Feature, ErrUnsupported, e.OS)
}

// Unwrap makes errors.Is(err, ErrUnsupported) hold.
func (e *UnsupportedError) Unwrap() error { return ErrUnsupported }

// Unsupported reports that feature has no implementation on the current
// host OS. The returned error wraps ErrUnsupported and names both the
// feature and the OS, e.g. "codesign inspection: unsupported on host OS
// (linux)".
func Unsupported(feature string) error {
	return &UnsupportedError{Feature: feature, OS: Current()}
}

// UnsupportedReport is the machine-readable form of an unsupported-feature
// failure, shared by `spectra <cmd> --json` and spectra-mcp tool errors so
// agents can tell "not on this OS" from "failed".
type UnsupportedReport struct {
	Error   string `json:"error"` // always "unsupported_platform"
	Feature string `json:"feature,omitempty"`
	OS      string `json:"os"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

// ReportUnsupported returns the report for err when it wraps
// ErrUnsupported, or for any of also (other packages' "not on this
// platform" sentinels); ok is false otherwise.
func ReportUnsupported(err error, also ...error) (UnsupportedReport, bool) {
	if err == nil {
		return UnsupportedReport{}, false
	}
	matched := errors.Is(err, ErrUnsupported)
	for _, sentinel := range also {
		matched = matched || errors.Is(err, sentinel)
	}
	if !matched {
		return UnsupportedReport{}, false
	}
	r := UnsupportedReport{Error: "unsupported_platform", OS: Current().String(), Message: err.Error()}
	var ue *UnsupportedError
	if errors.As(err, &ue) {
		r.Feature = ue.Feature
		r.OS = ue.OS.String()
	}
	return r, true
}

// CommandReport is what `spectra <cmd> --json` prints for a command with no
// implementation on this OS; the daemon sends the same object as the error
// data of the matching method.
type CommandReport struct {
	Command string `json:"command"`
	UnsupportedReport
}

// ReportCommandUnsupported is ReportUnsupported for command cmd with hint.
func ReportCommandUnsupported(cmd string, err error, hint string) (CommandReport, bool) {
	r, ok := ReportUnsupported(err)
	if !ok {
		return CommandReport{}, false
	}
	r.Hint = hint
	return CommandReport{Command: cmd, UnsupportedReport: r}, true
}
