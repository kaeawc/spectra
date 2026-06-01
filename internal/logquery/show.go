package logquery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultMaxRows   = 5000
	defaultMaxWindow = 24 * time.Hour
	defaultLast      = time.Hour
)

func Run(ctx context.Context, q Query) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	q = withDefaults(q)
	if err := validate(q); err != nil {
		return Result{}, err
	}
	args := buildArgs(q)
	started := time.Now()
	var result Result
	var stderr []byte
	var err error
	if q.Runner != nil {
		var stdout []byte
		stdout, stderr, err = q.Runner.Run(ctx, "/usr/bin/log", args...)
		result, _ = parseNDJSON(bytes.NewReader(stdout), q.MaxRows)
	} else {
		result, stderr, err = runLogShow(ctx, args, q.MaxRows)
	}
	result.QueryDuration = time.Since(started)
	result.Clock.WallClockAdjusted = bytes.Contains(stderr, []byte("Wall Clock adjustment detected"))
	if err != nil && len(result.Entries) == 0 {
		return result, err
	}
	return result, nil
}

func withDefaults(q Query) Query {
	if q.MaxRows <= 0 {
		q.MaxRows = defaultMaxRows
	}
	if q.MaxWindow <= 0 {
		q.MaxWindow = defaultMaxWindow
	}
	if q.Last == 0 && q.Start.IsZero() && q.End.IsZero() {
		q.Last = defaultLast
	}
	return q
}

func validate(q Query) error {
	if q.Predicate != "" && !q.AllowUnsafePredicate {
		return ErrUnsafePredicate
	}
	if q.AllowLongWindow {
		return nil
	}
	if q.Last > q.MaxWindow {
		return fmt.Errorf("%w: %s > %s", ErrWindowTooLarge, q.Last, q.MaxWindow)
	}
	if !q.Start.IsZero() && !q.End.IsZero() && q.End.Sub(q.Start) > q.MaxWindow {
		return fmt.Errorf("%w: %s > %s", ErrWindowTooLarge, q.End.Sub(q.Start), q.MaxWindow)
	}
	return nil
}

func buildArgs(q Query) []string {
	args := []string{"show", "--style", "ndjson", "--info"}
	if predicate := buildPredicate(q); predicate != "" {
		args = append(args, "--predicate", predicate)
	}
	if q.Last > 0 {
		args = append(args, "--last", durationArg(q.Last))
	} else {
		args = append(args, "--start", logTime(q.Start), "--end", logTime(q.End))
	}
	return args
}

func buildPredicate(q Query) string {
	var parts []string
	if q.Predicate != "" {
		parts = append(parts, "("+q.Predicate+")")
	}
	if q.Process != "" {
		parts = append(parts, `process == "`+escapePredicateString(q.Process)+`"`)
	}
	if q.Subsystem != "" {
		parts = append(parts, `subsystem == "`+escapePredicateString(q.Subsystem)+`"`)
	}
	if q.MinLevel != "" {
		parts = append(parts, minLevelPredicate(q.MinLevel))
	}
	return strings.Join(nonEmpty(parts), " AND ")
}

func minLevelPredicate(level string) string {
	switch strings.ToLower(level) {
	case "fault":
		return `messageType == "Fault"`
	case "error":
		return `(messageType == "Error" OR messageType == "Fault")`
	case "info":
		return `(messageType == "Info" OR messageType == "Default" OR messageType == "Error" OR messageType == "Fault")`
	default:
		return ""
	}
}

func escapePredicateString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func runLogShow(ctx context.Context, args []string, maxRows int) (Result, []byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/log", args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, nil, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, stderr.Bytes(), err
	}
	result, parseErr := parseNDJSON(stdout, maxRows)
	if result.Truncated && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return result, stderr.Bytes(), parseErr
	}
	return result, stderr.Bytes(), waitErr
}

func parseNDJSON(r io.Reader, maxRows int) (Result, error) {
	var result Result
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(result.Entries) >= maxRows {
			result.Truncated = true
			break
		}
		entry, err := parseLogEntry(scanner.Bytes())
		if err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, scanner.Err()
}

type rawEntry struct {
	Timestamp        string `json:"timestamp"`
	ProcessImagePath string `json:"processImagePath"`
	ProcessID        int32  `json:"processID"`
	SenderImagePath  string `json:"senderImagePath"`
	Subsystem        string `json:"subsystem"`
	Category         string `json:"category"`
	EventType        string `json:"eventType"`
	MessageType      string `json:"messageType"`
	ThreadID         uint64 `json:"threadID"`
	EventMessage     string `json:"eventMessage"`
	FormatString     string `json:"formatString"`
	ActivityID       uint64 `json:"activityIdentifier"`
	ParentActivityID uint64 `json:"parentActivityIdentifier"`
	TraceID          uint64 `json:"traceID"`
}

func parseLogEntry(data []byte) (LogEntry, error) {
	var raw rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return LogEntry{}, err
	}
	timestamp, _ := parseLogTimestamp(raw.Timestamp)
	return LogEntry{
		Timestamp:        timestamp,
		Process:          filepath.Base(raw.ProcessImagePath),
		PID:              raw.ProcessID,
		Sender:           filepath.Base(raw.SenderImagePath),
		SenderImagePath:  raw.SenderImagePath,
		Subsystem:        raw.Subsystem,
		Category:         raw.Category,
		EventType:        raw.EventType,
		LogType:          raw.MessageType,
		MessageType:      raw.MessageType,
		ThreadID:         raw.ThreadID,
		EventMessage:     raw.EventMessage,
		FormatString:     raw.FormatString,
		ActivityID:       raw.ActivityID,
		ParentActivityID: raw.ParentActivityID,
		TraceID:          raw.TraceID,
	}, nil
}

func parseLogTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999-0700",
		"2006-01-02 15:04:05.999999999-0700",
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q", value)
}

func durationArg(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func logTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}
