package jvm

import (
	"fmt"
	"strconv"
	"strings"
)

// JFRSummary is the structured form of `jfr summary <recording.jfr>`.
type JFRSummary struct {
	Path     string            `json:"path,omitempty"`
	Version  string            `json:"version,omitempty"`
	Chunks   int               `json:"chunks,omitempty"`
	Start    string            `json:"start,omitempty"`
	Duration string            `json:"duration,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Events   []JFREventSummary `json:"events,omitempty"`
}

// JFREventSummary is one event row from a JFR summary.
type JFREventSummary struct {
	Type      string `json:"type"`
	Count     int64  `json:"count"`
	SizeBytes int64  `json:"size_bytes"`
}

// SummarizeJFR runs the JDK `jfr summary` command and parses its output.
func SummarizeJFR(path string, run CmdRunner) (JFRSummary, error) {
	if run == nil {
		run = DefaultRunner
	}
	out, err := run("jfr", "summary", path)
	if err != nil {
		return JFRSummary{}, err
	}
	summary := ParseJFRSummary(path, string(out))
	return summary, nil
}

// ParseJFRSummary parses the stable text shape emitted by `jfr summary`.
func ParseJFRSummary(path, out string) JFRSummary {
	summary := JFRSummary{
		Path:     path,
		Metadata: make(map[string]string),
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(v)
			switch strings.ToLower(key) {
			case "version":
				summary.Version = val
			case "chunks":
				summary.Chunks, _ = strconv.Atoi(strings.ReplaceAll(val, ",", ""))
			case "start":
				summary.Start = val
			case "duration":
				summary.Duration = val
			default:
				summary.Metadata[key] = val
			}
			continue
		}
		if event, ok := parseJFREventSummary(line); ok {
			summary.Events = append(summary.Events, event)
		}
	}
	if len(summary.Metadata) == 0 {
		summary.Metadata = nil
	}
	return summary
}

func parseJFREventSummary(line string) (JFREventSummary, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return JFREventSummary{}, false
	}
	count, err := parseJFRInt(fields[len(fields)-2])
	if err != nil {
		return JFREventSummary{}, false
	}
	size, err := parseJFRInt(fields[len(fields)-1])
	if err != nil {
		return JFREventSummary{}, false
	}
	eventType := strings.Join(fields[:len(fields)-2], " ")
	if eventType == "" || eventType == "Event Type" {
		return JFREventSummary{}, false
	}
	return JFREventSummary{Type: eventType, Count: count, SizeBytes: size}, true
}

func parseJFRInt(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.ReplaceAll(s, ",", ""), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse jfr integer %q: %w", s, err)
	}
	return n, nil
}
