package rules

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"gopkg.in/yaml.v3"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// YAMLRuleSpec is the external rule format. YAML carries metadata and
// templates; CEL carries the side-effect-free predicate in Match.
type YAMLRuleSpec struct {
	ID       string   `yaml:"id"`
	Severity Severity `yaml:"severity"`
	ForEach  string   `yaml:"for_each,omitempty"`
	Match    string   `yaml:"match"`
	Subject  string   `yaml:"subject,omitempty"`
	Message  string   `yaml:"message"`
	Fix      string   `yaml:"fix,omitempty"`
	Tags     []string `yaml:"tags,omitempty"`

	source string
	line   int
}

// RuleDiagnostic describes a catalog validation problem with source context.
type RuleDiagnostic struct {
	Source string
	Line   int
	ID     string
	Err    error
}

func (d RuleDiagnostic) Error() string {
	var loc string
	switch {
	case d.Source != "" && d.Line > 0:
		loc = fmt.Sprintf("%s:%d", d.Source, d.Line)
	case d.Source != "":
		loc = d.Source
	default:
		loc = "rule"
	}
	if d.ID != "" {
		return fmt.Sprintf("%s: %s: %v", loc, d.ID, d.Err)
	}
	return fmt.Sprintf("%s: %v", loc, d.Err)
}

// LoadYAMLRules loads and compiles rules from YAML files.
func LoadYAMLRules(paths []string) ([]Rule, error) {
	var out []Rule
	seen := map[string]string{}
	for _, path := range paths {
		specs, err := LoadYAMLRuleSpecs(path)
		if err != nil {
			return nil, err
		}
		for _, spec := range specs {
			if prev, ok := seen[spec.ID]; ok {
				return nil, RuleDiagnostic{
					Source: spec.source,
					Line:   spec.line,
					ID:     spec.ID,
					Err:    fmt.Errorf("duplicate rule ID, already defined in %s", prev),
				}
			}
			rule, err := CompileYAMLRule(spec)
			if err != nil {
				return nil, err
			}
			seen[spec.ID] = spec.source
			out = append(out, rule)
		}
	}
	return out, nil
}

// MergeCatalogs appends extension rules to base after rejecting duplicate IDs.
// Metadata overrides remain the job of spectra.yml; executable rule replacement
// is intentionally explicit future work.
func MergeCatalogs(base []Rule, extension []Rule) ([]Rule, error) {
	seen := make(map[string]string, len(base))
	for _, rule := range base {
		source := rule.Source
		if source == "" {
			source = "built-in"
		}
		seen[rule.ID] = source
	}
	out := append([]Rule(nil), base...)
	for _, rule := range extension {
		if prev, ok := seen[rule.ID]; ok {
			return nil, RuleDiagnostic{
				Source: rule.Source,
				ID:     rule.ID,
				Err:    fmt.Errorf("duplicate rule ID, already defined in %s", prev),
			}
		}
		seen[rule.ID] = rule.Source
		out = append(out, rule)
	}
	return out, nil
}

// LoadYAMLRuleSpecs parses a YAML rule file. Supported top-level shapes are a
// list of rules or an object with a "rules" list.
func LoadYAMLRuleSpecs(path string) ([]YAMLRuleSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("%s: parse YAML: %w", path, err)
	}
	node := root.Content
	if len(node) == 0 {
		return nil, nil
	}
	rulesNode, err := yamlRulesNode(node[0])
	if err != nil {
		return nil, RuleDiagnostic{Source: path, Line: node[0].Line, Err: err}
	}
	var specs []YAMLRuleSpec
	for _, child := range rulesNode.Content {
		if err := rejectUnknownRuleFields(child); err != nil {
			return nil, RuleDiagnostic{Source: path, Line: child.Line, Err: err}
		}
		var spec YAMLRuleSpec
		if err := child.Decode(&spec); err != nil {
			return nil, RuleDiagnostic{Source: path, Line: child.Line, Err: err}
		}
		spec.source = path
		spec.line = child.Line
		if err := validateYAMLRuleSpec(spec); err != nil {
			return nil, RuleDiagnostic{Source: path, Line: child.Line, ID: spec.ID, Err: err}
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func rejectUnknownRuleFields(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("rule must be a mapping")
	}
	allowed := map[string]struct{}{
		"id": {}, "severity": {}, "for_each": {}, "match": {}, "subject": {},
		"message": {}, "fix": {}, "tags": {},
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown rule field %q", key)
		}
	}
	return nil
}

func yamlRulesNode(root *yaml.Node) (*yaml.Node, error) {
	switch root.Kind {
	case yaml.SequenceNode:
		return root, nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == "rules" {
				if root.Content[i+1].Kind != yaml.SequenceNode {
					return nil, fmt.Errorf("rules must be a list")
				}
				return root.Content[i+1], nil
			}
		}
		return nil, fmt.Errorf("top-level mapping must contain rules")
	default:
		return nil, fmt.Errorf("top-level YAML must be a rule list or {rules: [...]}")
	}
}

func validateYAMLRuleSpec(spec YAMLRuleSpec) error {
	switch {
	case strings.TrimSpace(spec.ID) == "":
		return fmt.Errorf("id is required")
	case strings.TrimSpace(spec.Match) == "":
		return fmt.Errorf("match is required")
	case strings.TrimSpace(spec.Message) == "":
		return fmt.Errorf("message is required")
	}
	if _, err := parseSeverity(string(spec.Severity)); err != nil {
		return err
	}
	if spec.ForEach != "" && !knownCollection(spec.ForEach) {
		return fmt.Errorf("unsupported for_each %q", spec.ForEach)
	}
	return nil
}

func knownCollection(name string) bool {
	switch name {
	case "apps", "processes", "jvms", "toolchains.jdks":
		return true
	default:
		return false
	}
}

// CompileYAMLRule compiles one YAML rule spec into the internal executable
// Rule shape consumed by the existing recommendations engine.
func CompileYAMLRule(spec YAMLRuleSpec) (Rule, error) {
	env, err := NewCELEnv()
	if err != nil {
		return Rule{}, err
	}
	ast, issues := env.Compile(spec.Match)
	if issues != nil && issues.Err() != nil {
		return Rule{}, RuleDiagnostic{Source: spec.source, Line: spec.line, ID: spec.ID, Err: issues.Err()}
	}
	prg, err := env.Program(ast)
	if err != nil {
		return Rule{}, RuleDiagnostic{Source: spec.source, Line: spec.line, ID: spec.ID, Err: err}
	}
	messageTemplate, err := parseRuleTemplate(spec, "message", spec.Message)
	if err != nil {
		return Rule{}, err
	}
	fixTemplate, err := parseRuleTemplate(spec, "fix", spec.Fix)
	if err != nil {
		return Rule{}, err
	}
	subjectTemplate, err := parseRuleTemplate(spec, "subject", spec.Subject)
	if err != nil {
		return Rule{}, err
	}
	return Rule{
		ID:       spec.ID,
		Severity: spec.Severity,
		Source:   spec.source,
		Message:  spec.Message,
		Fix:      spec.Fix,
		MatchFn: func(s snapshot.Snapshot) []Finding {
			return evalYAMLRule(s, spec, prg, messageTemplate, fixTemplate, subjectTemplate)
		},
	}, nil
}

func parseRuleTemplate(spec YAMLRuleSpec, field string, raw string) (*template.Template, error) {
	if raw == "" {
		return nil, nil
	}
	tmpl, err := template.New(field).Option("missingkey=zero").Parse(raw)
	if err != nil {
		return nil, RuleDiagnostic{
			Source: spec.source,
			Line:   spec.line,
			ID:     spec.ID,
			Err:    fmt.Errorf("%s template: %w", field, err),
		}
	}
	return tmpl, nil
}

func evalYAMLRule(s snapshot.Snapshot, spec YAMLRuleSpec, prg cel.Program, messageTemplate, fixTemplate, subjectTemplate *template.Template) []Finding {
	activation := SnapshotActivation(s)
	items := []any{nil}
	if spec.ForEach != "" {
		items = collectionItems(activation, spec.ForEach)
	}
	out := make([]Finding, 0)
	for _, item := range items {
		ruleActivation := copyActivation(activation)
		if item != nil {
			ruleActivation["item"] = item
		}
		ok, err := evalCELBool(prg, ruleActivation)
		if err != nil || !ok {
			continue
		}
		data := templateData(ruleActivation)
		out = append(out, Finding{
			RuleID:   spec.ID,
			Severity: spec.Severity,
			Message:  renderRuleTemplate(messageTemplate, data),
			Fix:      renderRuleTemplate(fixTemplate, data),
			Subject:  renderRuleTemplate(subjectTemplate, data),
		})
	}
	return out
}

func evalCELBool(prg cel.Program, activation map[string]any) (bool, error) {
	val, _, err := prg.Eval(activation)
	if err != nil {
		return false, err
	}
	if val == nil {
		return false, nil
	}
	if native, ok := val.Value().(bool); ok {
		return native, nil
	}
	return false, fmt.Errorf("match returned %T, want bool", val.Value())
}

func collectionItems(activation map[string]any, path string) []any {
	var value any = activation
	for _, part := range strings.Split(path, ".") {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[part]
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	return items
}

func copyActivation(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func templateData(activation map[string]any) map[string]any {
	data := make(map[string]any, len(activation))
	for k, v := range activation {
		data[k] = v
	}
	return data
}

func renderRuleTemplate(tmpl *template.Template, data map[string]any) string {
	if tmpl == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// NewCELEnv returns the CEL environment shared by YAML rules.
func NewCELEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("snapshot", cel.DynType),
		cel.Variable("host", cel.DynType),
		cel.Variable("apps", cel.DynType),
		cel.Variable("processes", cel.DynType),
		cel.Variable("jvms", cel.DynType),
		cel.Variable("toolchains", cel.DynType),
		cel.Variable("network", cel.DynType),
		cel.Variable("storage", cel.DynType),
		cel.Variable("power", cel.DynType),
		cel.Variable("sysctls", cel.DynType),
		cel.Variable("item", cel.DynType),
		cel.Function("percent",
			cel.Overload("percent_dyn_dyn",
				[]*cel.Type{cel.DynType, cel.DynType},
				cel.DoubleType,
				cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
					denominator, ok := celNumber(rhs.Value())
					if !ok || denominator == 0 {
						return types.Double(0)
					}
					numerator, ok := celNumber(lhs.Value())
					if !ok {
						return types.Double(0)
					}
					return types.Double(numerator / denominator * 100)
				}),
			),
		),
		cel.Function("bytesGB",
			cel.Overload("bytes_gb_dyn",
				[]*cel.Type{cel.DynType},
				cel.DoubleType,
				cel.UnaryBinding(func(value ref.Val) ref.Val {
					n, ok := celNumber(value.Value())
					if !ok {
						return types.Double(0)
					}
					return types.Double(n / 1024 / 1024 / 1024)
				}),
			),
		),
		cel.Function("semverCompare",
			cel.Overload("semver_compare_string_string",
				[]*cel.Type{cel.StringType, cel.StringType},
				cel.IntType,
				cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
					return types.Int(compareVersionStrings(fmt.Sprint(lhs.Value()), fmt.Sprint(rhs.Value())))
				}),
			),
		),
	)
}

func celNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func compareVersionStrings(a, b string) int {
	left := versionParts(a)
	right := versionParts(b)
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	for i := 0; i < n; i++ {
		var lv, rv int
		if i < len(left) {
			lv = left[i]
		}
		if i < len(right) {
			rv = right[i]
		}
		switch {
		case lv < rv:
			return -1
		case lv > rv:
			return 1
		}
	}
	return 0
}

func versionParts(raw string) []int {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r < '0' || r > '9'
	})
	out := make([]int, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		n, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// CatalogSummary is a stable, printable view of a loaded rule catalog.
type CatalogSummary struct {
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Source   string   `json:"source,omitempty"`
	Message  string   `json:"message,omitempty"`
	Fix      string   `json:"fix,omitempty"`
}

func SummarizeCatalog(catalog []Rule) []CatalogSummary {
	out := make([]CatalogSummary, 0, len(catalog))
	for _, rule := range catalog {
		source := rule.Source
		if source == "" {
			source = "built-in"
		}
		out = append(out, CatalogSummary{
			ID:       rule.ID,
			Severity: rule.Severity,
			Source:   source,
			Message:  rule.Message,
			Fix:      rule.Fix,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Source < out[j].Source
	})
	return out
}

// SplitRulePaths parses comma-separated rule path flags.
func SplitRulePaths(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if matches, err := filepath.Glob(part); err == nil && len(matches) > 0 {
			out = append(out, matches...)
			continue
		}
		out = append(out, part)
	}
	return out
}
