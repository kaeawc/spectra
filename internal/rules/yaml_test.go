package rules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func TestYAMLRuleForEachJVM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(path, []byte(`
rules:
  - id: yaml-jvm-eol
    severity: medium
    for_each: jvms
    match: item.version_major < 11
    subject: "jvm:{{ .item.pid }}"
    message: "JDK {{ .item.jdk_version }} is old"
    fix: "Upgrade {{ .item.main_class }}"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	catalog, err := LoadYAMLRules([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Snapshot{
		JVMs: []jvm.Info{
			{PID: 1, MainClass: "ok.App", JDKVersion: "17.0.10"},
			{PID: 2, MainClass: "old.App", JDKVersion: "1.8.0_402"},
		},
	}
	findings := Evaluate(s, catalog)
	if len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1: %#v", len(findings), findings)
	}
	if findings[0].Subject != "jvm:2" {
		t.Fatalf("subject = %q, want jvm:2", findings[0].Subject)
	}
	if findings[0].Message != "JDK 1.8.0_402 is old" {
		t.Fatalf("message = %q", findings[0].Message)
	}
}

func TestYAMLRuleHostLevel(t *testing.T) {
	spec := YAMLRuleSpec{
		ID:       "yaml-low-ram",
		Severity: SeverityLow,
		Match:    "host.ram_mb < 8192",
		Message:  "Host {{ .host.hostname }} has low RAM",
	}
	rule, err := CompileYAMLRule(spec)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Snapshot{
		Host: snapshot.HostInfo{
			Hostname: "tiny",
			RAMBytes: 4 * 1024 * 1024 * 1024,
		},
	}
	findings := rule.MatchFn(s)
	if len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1", len(findings))
	}
	if findings[0].Message != "Host tiny has low RAM" {
		t.Fatalf("message = %q", findings[0].Message)
	}
}

func TestYAMLRuleHelperFunctions(t *testing.T) {
	spec := YAMLRuleSpec{
		ID:       "yaml-helper-functions",
		Severity: SeverityInfo,
		Match:    `percent(host.ram_bytes, 8 * 1024 * 1024 * 1024) == 50.0 && bytesGB(host.ram_bytes) == 4.0 && semverCompare("21.0.6", "17.0.10") > 0`,
		Message:  "helpers work",
	}
	rule, err := CompileYAMLRule(spec)
	if err != nil {
		t.Fatal(err)
	}
	s := snapshot.Snapshot{Host: snapshot.HostInfo{RAMBytes: 4 * 1024 * 1024 * 1024}}
	if findings := rule.MatchFn(s); len(findings) != 1 {
		t.Fatalf("len(findings) = %d, want 1: %#v", len(findings), findings)
	}
}

func TestCompareVersionStrings(t *testing.T) {
	if got := compareVersionStrings("21.0.6", "17.0.10"); got <= 0 {
		t.Fatalf("compareVersionStrings newer = %d, want > 0", got)
	}
	if got := compareVersionStrings("1.8.0_402", "8.0.402"); got != -1 {
		t.Fatalf("legacy compare = %d, want -1 with raw numeric comparison", got)
	}
}

func TestLoadYAMLRulesRejectsDuplicateIDs(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.yml")
	b := filepath.Join(dir, "b.yml")
	body := []byte(`
- id: dup
  severity: info
  match: "true"
  message: "duplicate"
`)
	if err := os.WriteFile(a, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadYAMLRules([]string{a, b}); err == nil {
		t.Fatal("LoadYAMLRules succeeded, want duplicate ID error")
	}
}

func TestLoadYAMLRulesRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(path, []byte(`
- id: bad
  severity: info
  match: "true"
  message: "bad"
  typo: true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadYAMLRules([]string{path}); err == nil {
		t.Fatal("LoadYAMLRules succeeded, want unknown field error")
	}
}

func TestMergeCatalogsRejectsDuplicateBuiltInID(t *testing.T) {
	_, err := MergeCatalogs([]Rule{{ID: "built-in", Severity: SeverityInfo}}, []Rule{{ID: "built-in", Severity: SeverityLow, Source: "rules.yml"}})
	if err == nil {
		t.Fatal("MergeCatalogs succeeded, want duplicate ID error")
	}
}

func TestLoadCatalogSources(t *testing.T) {
	catalog, err := LoadCatalog(
		staticCatalogSource{name: "one", rules: []Rule{{ID: "one", Severity: SeverityInfo}}},
		staticCatalogSource{name: "two", rules: []Rule{{ID: "two", Severity: SeverityLow}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("len(catalog) = %d, want 2", len(catalog))
	}
}

type staticCatalogSource struct {
	name  string
	rules []Rule
}

func (s staticCatalogSource) Name() string {
	return s.name
}

func (s staticCatalogSource) LoadRules() ([]Rule, error) {
	return s.rules, nil
}
