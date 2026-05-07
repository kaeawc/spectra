package jvm

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/recording"
)

const (
	JFRViewGCPauses       = "gc-pauses"
	JFRViewAllocationSite = "allocation-by-site"
	JFRViewHotMethods     = "hot-methods"
	JFRViewMonitorBlocked = "monitor-blocked"
	JFRViewFileIO         = "file-io"
	JFRViewSocketIO       = "socket-io"
)

var jfrColumnSplit = regexp.MustCompile(`\s{2,}`)

// JFRViewTable is one tabular section emitted by `jfr view`.
type JFRViewTable = recording.Table

// JFRViewTableRow is one row from a `jfr view` table.
type JFRViewTableRow = recording.Row

// JFRViewResult is the structured form of one `jfr view <view> <recording>`.
type JFRViewResult = recording.View

// JFRAnalysisOptions controls which reusable JFR event views are collected.
type JFRAnalysisOptions struct {
	Views []string
}

// JFRAnalysis combines summary data, event-view tables, and derived incident notes.
type JFRAnalysis = recording.Analysis

// JFRFinding is a human-oriented observation derived from a recording.
type JFRFinding = recording.Finding

// JFRAnalyzer implements recording.Analyzer for Java Flight Recorder files.
type JFRAnalyzer struct {
	Run   CmdRunner
	Views []string
}

// ViewJFR runs `jfr view` for one event view and parses tabular output.
func ViewJFR(path, view string, run CmdRunner) (JFRViewResult, error) {
	if run == nil {
		run = DefaultRunner
	}
	out, err := run("jfr", "view", view, path)
	if err != nil {
		return JFRViewResult{}, err
	}
	result := ParseJFRView(path, view, string(out))
	return result, nil
}

// AnalyzeRecording implements recording.Analyzer for JFR recordings.
func (a JFRAnalyzer) AnalyzeRecording(path string, opts recording.Options) (recording.Analysis, error) {
	views := opts.Views
	if len(views) == 0 {
		views = a.Views
	}
	return AnalyzeJFR(path, JFRAnalysisOptions{Views: views}, a.Run)
}

// AnalyzeJFR collects reusable event views and derives first-pass incident notes.
func AnalyzeJFR(path string, opts JFRAnalysisOptions, run CmdRunner) (JFRAnalysis, error) {
	if run == nil {
		run = DefaultRunner
	}
	views := opts.Views
	if len(views) == 0 {
		views = defaultJFRAnalysisViews()
	}
	summary, err := SummarizeJFR(path, run)
	if err != nil {
		return JFRAnalysis{}, fmt.Errorf("summarize jfr: %w", err)
	}
	analysis := JFRAnalysis{
		Artifact: recording.Artifact{
			Path:         path,
			Kind:         "jfr",
			Runtime:      "jvm",
			Architecture: "java",
		},
		Summary: summary,
	}
	for _, view := range views {
		result, err := ViewJFR(path, view, run)
		if err != nil {
			return JFRAnalysis{}, fmt.Errorf("view %s: %w", view, err)
		}
		analysis.Views = append(analysis.Views, result)
	}
	analysis.Narrative = BuildJFRNarrative(summary, analysis.Views)
	return analysis, nil
}

// ParseJFRView parses the text tables emitted by `jfr view`.
func ParseJFRView(path, view, out string) JFRViewResult {
	result := JFRViewResult{
		Path: path,
		View: view,
		Raw:  out,
	}
	lines := strings.Split(out, "\n")
	var title string
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if isJFRSeparator(line) && i > 0 {
			header := strings.TrimSpace(lines[i-1])
			columns := splitJFRColumns(header)
			if len(columns) < 2 {
				continue
			}
			table := JFRViewTable{
				Title:   title,
				Columns: columns,
			}
			for j := i + 1; j < len(lines); j++ {
				rowLine := strings.TrimSpace(lines[j])
				if rowLine == "" {
					i = j
					break
				}
				if isJFRSeparator(rowLine) {
					i = j - 1
					break
				}
				values := splitJFRColumns(rowLine)
				if len(values) == 0 {
					continue
				}
				row := make(JFRViewTableRow)
				for idx, column := range columns {
					if idx >= len(values) {
						break
					}
					row[column] = values[idx]
				}
				if len(row) > 0 {
					table.Rows = append(table.Rows, row)
				}
				if j == len(lines)-1 {
					i = j
				}
			}
			result.Tables = append(result.Tables, table)
			title = ""
			continue
		}
		if !isLikelyJFRHeader(line) {
			title = line
		}
	}
	return result
}

// BuildJFRNarrative turns event-counts and parsed views into reusable incident notes.
func BuildJFRNarrative(summary JFRSummary, views []JFRViewResult) []JFRFinding {
	var findings []JFRFinding
	counts := jfrEventCounts(summary.Events)
	if counts["jdk.GarbageCollection"] > 0 || counts["jdk.GCPhasePause"] > 0 {
		findings = append(findings, JFRFinding{
			Area:     "gc",
			Severity: "info",
			Summary:  "GC pause events are present in the recording.",
			Detail:   fmt.Sprintf("GarbageCollection=%d GCPhasePause=%d", counts["jdk.GarbageCollection"], counts["jdk.GCPhasePause"]),
		})
	}
	if counts["jdk.ObjectAllocationInNewTLAB"] > 0 || counts["jdk.ObjectAllocationOutsideTLAB"] > 0 {
		findings = append(findings, JFRFinding{
			Area:     "allocation",
			Severity: "info",
			Summary:  "Allocation events are present and can be correlated with pause windows.",
			Detail:   fmt.Sprintf("ObjectAllocationInNewTLAB=%d ObjectAllocationOutsideTLAB=%d", counts["jdk.ObjectAllocationInNewTLAB"], counts["jdk.ObjectAllocationOutsideTLAB"]),
		})
	}
	for _, view := range views {
		top := firstJFRRow(view)
		if top == nil {
			continue
		}
		switch view.View {
		case JFRViewGCPauses:
			findings = append(findings, findingFromRow("gc", "info", "Top GC pause view has entries.", top))
		case JFRViewAllocationSite:
			findings = append(findings, findingFromRow("allocation", "info", "Top allocation site view has entries.", top))
		case JFRViewHotMethods:
			findings = append(findings, findingFromRow("methods", "info", "Method profiling samples are present.", top))
		case JFRViewMonitorBlocked:
			findings = append(findings, findingFromRow("locks", "warning", "Monitor blocking events are present.", top))
		case JFRViewFileIO:
			findings = append(findings, findingFromRow("file-io", "info", "File I/O events are present.", top))
		case JFRViewSocketIO:
			findings = append(findings, findingFromRow("socket-io", "info", "Socket I/O events are present.", top))
		}
	}
	return findings
}

func defaultJFRAnalysisViews() []string {
	return []string{
		JFRViewGCPauses,
		JFRViewAllocationSite,
		JFRViewHotMethods,
		JFRViewMonitorBlocked,
		JFRViewFileIO,
		JFRViewSocketIO,
	}
}

func splitJFRColumns(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return jfrColumnSplit.Split(line, -1)
}

func isJFRSeparator(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if r != '-' && r != ' ' {
			return false
		}
	}
	return strings.Contains(line, "---")
}

func isLikelyJFRHeader(line string) bool {
	return len(splitJFRColumns(line)) > 1
}

func jfrEventCounts(events []JFREventSummary) map[string]int64 {
	counts := make(map[string]int64, len(events))
	for _, event := range events {
		counts[event.Type] = event.Count
	}
	return counts
}

func firstJFRRow(view JFRViewResult) JFRViewTableRow {
	for _, table := range view.Tables {
		if len(table.Rows) > 0 {
			return table.Rows[0]
		}
	}
	return nil
}

func findingFromRow(area, severity, summary string, row JFRViewTableRow) JFRFinding {
	return JFRFinding{
		Area:     area,
		Severity: severity,
		Summary:  summary,
		Detail:   formatJFRRow(row),
	}
}

func formatJFRRow(row JFRViewTableRow) string {
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+row[key])
	}
	return strings.Join(parts, " ")
}
