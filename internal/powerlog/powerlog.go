// Package powerlog parses macOS power history from pmset. It reads the
// sleep/wake log (pmset -g log) and the current sleep-blocking assertions
// (pmset -g assertions) — both without root — to answer "why did my Mac wake,
// not sleep, or run hot overnight?" The parsers are deliberately tolerant: they
// key on the entry type and skip lines they don't recognize rather than failing.
package powerlog

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner runs a command and returns combined stdout. Injected for testability.
type Runner func(name string, args ...string) ([]byte, error)

// DefaultRunner runs the real command.
func DefaultRunner(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

// Event is one sleep/wake power event.
type Event struct {
	Time    time.Time `json:"time,omitempty"`
	RawTime string    `json:"raw_time"`
	Type    string    `json:"type"`
	Detail  string    `json:"detail,omitempty"`
}

// SleepBlocker is a process currently holding a sleep-preventing assertion.
type SleepBlocker struct {
	PID     int    `json:"pid"`
	Process string `json:"process"`
	Type    string `json:"type"`
	Held    string `json:"held,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// Report is the parsed power history.
type Report struct {
	Events        []Event        `json:"events"`
	SleepBlockers []SleepBlocker `json:"sleep_blockers"`
}

var (
	logLineRe   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [-+]\d{4})\s+(\S+)\s*(.*)$`)
	assertionRe = regexp.MustCompile(`pid (\d+)\(([^)]+)\):\s+\[[^\]]+\]\s+(\S+)\s+(\S+)\s+named:\s+"([^"]*)"`)
)

// Collect runs pmset and parses the sleep/wake log and current sleep blockers.
func Collect(run Runner) (*Report, error) {
	logOut, err := run("pmset", "-g", "log")
	if err != nil {
		return nil, fmt.Errorf("pmset -g log: %w", err)
	}
	assertOut, err := run("pmset", "-g", "assertions")
	if err != nil {
		return nil, fmt.Errorf("pmset -g assertions: %w", err)
	}
	return &Report{
		Events:        ParseLog(string(logOut)),
		SleepBlockers: ParseAssertions(string(assertOut)),
	}, nil
}

// ParseLog extracts sleep/wake/darkwake events from `pmset -g log` output.
func ParseLog(text string) []Event {
	var events []Event
	for _, line := range strings.Split(text, "\n") {
		m := logLineRe.FindStringSubmatch(line)
		if m == nil || !isSleepWakeType(m[2]) {
			continue
		}
		detail := strings.TrimSpace(m[3])
		// "Wake Requests" lines list *scheduled* wake requests, not an actual
		// wake — the leading token collides with real Wake events.
		if m[2] == "Wake" && strings.HasPrefix(detail, "Requests") {
			continue
		}
		e := Event{RawTime: m[1], Type: m[2], Detail: detail}
		if t, err := time.Parse("2006-01-02 15:04:05 -0700", m[1]); err == nil {
			e.Time = t
		}
		events = append(events, e)
	}
	return events
}

// ParseAssertions extracts current sleep-blocking assertion holders from
// `pmset -g assertions` output.
func ParseAssertions(text string) []SleepBlocker {
	var out []SleepBlocker
	for _, line := range strings.Split(text, "\n") {
		m := assertionRe.FindStringSubmatch(line)
		if m == nil || !isSleepBlocking(m[4]) {
			continue
		}
		pid, _ := strconv.Atoi(m[1])
		out = append(out, SleepBlocker{
			PID:     pid,
			Process: m[2],
			Held:    m[3],
			Type:    m[4],
			Reason:  m[5],
		})
	}
	return out
}

func isSleepWakeType(t string) bool {
	switch t {
	case "Sleep", "Wake", "DarkWake":
		return true
	default:
		return false
	}
}

func isSleepBlocking(t string) bool {
	return t == "PreventUserIdleSystemSleep" || t == "PreventSystemSleep"
}
