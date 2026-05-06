package rules

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// Overrides are project-local adjustments loaded from spectra.yml.
type Overrides struct {
	Disabled map[string]struct{}
	Severity map[string]Severity
}

// Empty reports whether no overrides were configured.
func (o Overrides) Empty() bool {
	return len(o.Disabled) == 0 && len(o.Severity) == 0
}

// LoadOverrides reads a project-local rules config.
func LoadOverrides(path string) (Overrides, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Overrides{}, err
	}
	return ParseOverrides(data)
}

// ParseOverrides parses the supported spectra.yml rules subset:
//
//	rules:
//	  disabled:
//	    - app-unsigned
//	  severity:
//	    jvm-eol-version: high
func ParseOverrides(data []byte) (Overrides, error) {
	out := Overrides{
		Disabled: map[string]struct{}{},
		Severity: map[string]Severity{},
	}
	var section string
	var mode string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for lineNo := 1; scanner.Scan(); lineNo++ {
		nextSection, nextMode, ok, err := parseOverrideLine(scanner.Text(), lineNo, section, mode, &out)
		if err != nil {
			return Overrides{}, err
		}
		if !ok {
			continue
		}
		section, mode = nextSection, nextMode
	}
	if err := scanner.Err(); err != nil {
		return Overrides{}, err
	}
	return out, nil
}

func parseOverrideLine(raw string, lineNo int, section string, mode string, out *Overrides) (string, string, bool, error) {
	line := stripYAMLComment(raw)
	if strings.TrimSpace(line) == "" {
		return section, mode, false, nil
	}
	indent := leadingSpaces(line)
	trimmed := strings.TrimSpace(line)
	if indent == 0 {
		return parseTopLevelYAMLKey(trimmed), "", true, nil
	}
	if section != "rules" {
		return section, mode, false, nil
	}
	if indent == 2 {
		nextMode := parseRulesMode(trimmed)
		if nextMode == "" {
			return "", "", false, fmt.Errorf("line %d: unsupported rules key %q", lineNo, trimmed)
		}
		return section, nextMode, true, nil
	}
	if err := parseRulesOverrideValue(trimmed, lineNo, mode, out); err != nil {
		return "", "", false, err
	}
	return section, mode, true, nil
}

func parseRulesOverrideValue(trimmed string, lineNo int, mode string, out *Overrides) error {
	switch mode {
	case "disabled":
		id, ok := strings.CutPrefix(trimmed, "- ")
		if !ok || strings.TrimSpace(id) == "" {
			return fmt.Errorf("line %d: disabled rule must be '- <rule-id>'", lineNo)
		}
		out.Disabled[strings.TrimSpace(id)] = struct{}{}
		return nil
	case "severity":
		id, rawSeverity, ok := strings.Cut(trimmed, ":")
		if !ok || strings.TrimSpace(id) == "" || strings.TrimSpace(rawSeverity) == "" {
			return fmt.Errorf("line %d: severity override must be '<rule-id>: <severity>'", lineNo)
		}
		severity, err := parseSeverity(strings.TrimSpace(rawSeverity))
		if err != nil {
			return fmt.Errorf("line %d: %w", lineNo, err)
		}
		out.Severity[strings.TrimSpace(id)] = severity
		return nil
	default:
		return fmt.Errorf("line %d: unsupported rules override syntax", lineNo)
	}
}

// ApplyOverrides returns a catalog with disabled rules removed and severity
// overrides applied to both the Rule metadata and emitted findings.
func ApplyOverrides(catalog []Rule, overrides Overrides) []Rule {
	if overrides.Empty() {
		return catalog
	}
	out := make([]Rule, 0, len(catalog))
	for _, rule := range catalog {
		if _, disabled := overrides.Disabled[rule.ID]; disabled {
			continue
		}
		if severity, ok := overrides.Severity[rule.ID]; ok {
			rule.Severity = severity
			match := rule.MatchFn
			rule.MatchFn = func(snap snapshot.Snapshot) []Finding {
				findings := match(snap)
				for i := range findings {
					findings[i].Severity = severity
				}
				return findings
			}
		}
		out = append(out, rule)
	}
	return out
}

// OverrideWarnings returns human-readable warnings for override IDs that do
// not match a rule in catalog.
func OverrideWarnings(overrides Overrides, catalog []Rule) []string {
	if overrides.Empty() {
		return nil
	}
	known := make(map[string]struct{}, len(catalog))
	for _, rule := range catalog {
		known[rule.ID] = struct{}{}
	}
	ids := make(map[string]struct{}, len(overrides.Disabled)+len(overrides.Severity))
	for id := range overrides.Disabled {
		ids[id] = struct{}{}
	}
	for id := range overrides.Severity {
		ids[id] = struct{}{}
	}
	var warnings []string
	for id := range ids {
		if _, ok := known[id]; !ok {
			warnings = append(warnings, fmt.Sprintf("rules override references unknown rule %q", id))
		}
	}
	sort.Strings(warnings)
	return warnings
}

func stripYAMLComment(line string) string {
	if idx := strings.Index(line, "#"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func parseTopLevelYAMLKey(line string) string {
	if strings.HasSuffix(line, ":") {
		return strings.TrimSuffix(line, ":")
	}
	return ""
}

func parseRulesMode(line string) string {
	switch line {
	case "disabled:":
		return "disabled"
	case "severity:":
		return "severity"
	default:
		return ""
	}
}

func parseSeverity(raw string) (Severity, error) {
	switch Severity(raw) {
	case SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo:
		return Severity(raw), nil
	default:
		return "", fmt.Errorf("invalid severity %q", raw)
	}
}
