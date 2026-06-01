package updates

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var ErrNeedsFullDiskAccess = errors.New("needs full disk access")

const FullDiskAccessRemediation = "Grant Full Disk Access to the terminal running Spectra, then retry."

type InstallLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Hostname  string    `json:"hostname,omitempty"`
	Process   string    `json:"process,omitempty"`
	PID       int32     `json:"pid,omitempty"`
	Message   string    `json:"message"`
}

type Query struct {
	Since    time.Time
	Until    time.Time
	Process  string
	Grep     string
	MaxLines int
	Paths    []string
}

type Result struct {
	Entries   []InstallLogEntry `json:"entries"`
	History   InstallHistory    `json:"history,omitempty"`
	FilesRead []string          `json:"files_read"`
	Truncated bool              `json:"truncated,omitempty"`
}

type MajorUpdatePrepared struct {
	AssetID    string    `json:"asset_id"`
	Title      string    `json:"title,omitempty"`
	Version    string    `json:"version,omitempty"`
	SizeBytes  uint64    `json:"size_bytes,omitempty"`
	PreparedAt time.Time `json:"prepared_at,omitempty"`
}

type OSControllerTransition struct {
	PreviousLabel string    `json:"previous_label"`
	CurrentLabel  string    `json:"current_label"`
	At            time.Time `json:"at,omitempty"`
}

func QueryInstallLog(q Query) (Result, error) {
	if q.MaxLines <= 0 {
		q.MaxLines = 5000
	}
	paths := q.Paths
	if len(paths) == 0 {
		paths = defaultInstallLogPaths()
	}
	var result Result
	for _, path := range paths {
		entries, err := readInstallLogFile(path, q)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return result, ErrNeedsFullDiskAccess
			}
			continue
		}
		result.FilesRead = append(result.FilesRead, path)
		for _, entry := range entries {
			if len(result.Entries) >= q.MaxLines {
				result.Truncated = true
				return result, nil
			}
			result.Entries = append(result.Entries, entry)
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		return result.Entries[i].Timestamp.Before(result.Entries[j].Timestamp)
	})
	return result, nil
}

func defaultInstallLogPaths() []string {
	paths := []string{"/var/log/install.log"}
	matches, _ := filepath.Glob("/var/log/install.log.*.gz")
	sort.Strings(matches)
	paths = append(paths, matches...)
	return paths
}

func readInstallLogFile(path string, q Query) ([]InstallLogEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		reader = gz
	}
	return parseInstallLog(reader, q), nil
}

var installLineRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})([-+]\d{2}) ([^ ]+) ([^\[]+)\[(\d+)\]: (.*)$`)

func parseInstallLog(r io.Reader, q Query) []InstallLogEntry {
	var entries []InstallLogEntry
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		entry, ok := ParseInstallLogLine(scanner.Text())
		if !ok || !matchesQuery(entry, q) {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func ParseInstallLogLine(line string) (InstallLogEntry, bool) {
	match := installLineRE.FindStringSubmatch(line)
	if len(match) != 7 {
		return InstallLogEntry{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05-07", match[1]+match[2])
	if err != nil {
		return InstallLogEntry{}, false
	}
	pid := int32(0)
	if n, err := parseInt32(match[5]); err == nil {
		pid = n
	}
	return InstallLogEntry{
		Timestamp: t,
		Hostname:  match[3],
		Process:   match[4],
		PID:       pid,
		Message:   match[6],
	}, true
}

func matchesQuery(entry InstallLogEntry, q Query) bool {
	if !q.Since.IsZero() && entry.Timestamp.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && entry.Timestamp.After(q.Until) {
		return false
	}
	if q.Process != "" && entry.Process != q.Process {
		return false
	}
	return q.Grep == "" || strings.Contains(entry.Message, q.Grep)
}

func parseInt32(s string) (int32, error) {
	var n int32
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid int32")
		}
		n = n*10 + c - '0'
	}
	return n, nil
}

var majorPreparedRE = regexp.MustCompile(`<SUOSUMajorProduct:\s*([^>]+)>\(Title:([^ ]+(?: [^ ]+)*) Version:([^,\)]+).*?\)\s+is already prepared`)
var majorSizeRE = regexp.MustCompile(`(?:^|[ ,])Size:(\d+)`)

func ParseMajorUpdatePrepared(line string) (MajorUpdatePrepared, bool) {
	entry, _ := ParseInstallLogLine(line)
	text := line
	if entry.Message != "" {
		text = entry.Message
	}
	match := majorPreparedRE.FindStringSubmatch(text)
	if len(match) != 4 {
		return MajorUpdatePrepared{}, false
	}
	out := MajorUpdatePrepared{
		AssetID:    strings.TrimSpace(match[1]),
		Title:      strings.TrimSpace(match[2]),
		Version:    strings.TrimSpace(match[3]),
		PreparedAt: entry.Timestamp,
	}
	if sizeMatch := majorSizeRE.FindStringSubmatch(text); len(sizeMatch) == 2 {
		out.SizeBytes = parseUint(sizeMatch[1])
	}
	return out, true
}

var controllerLabelRE = regexp.MustCompile(`SFR:([^)]*) \((?:Customer|Development)\)`)

func ParseOSControllerTransition(line string) (OSControllerTransition, bool) {
	if !strings.Contains(line, "Previous:") && !strings.Contains(line, "Current:") {
		return OSControllerTransition{}, false
	}
	previous := ""
	current := ""
	if strings.Contains(line, "Previous:") {
		previous = controllerLabel(line)
	}
	if strings.Contains(line, "Current:") {
		current = controllerLabel(line)
	}
	if previous == "" && current == "" {
		return OSControllerTransition{}, false
	}
	entry, _ := ParseInstallLogLine(line)
	return OSControllerTransition{PreviousLabel: previous, CurrentLabel: current, At: entry.Timestamp}, true
}

func controllerLabel(line string) string {
	match := controllerLabelRE.FindStringSubmatch(line)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func parseUint(s string) uint64 {
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + uint64(c-'0')
	}
	return n
}
