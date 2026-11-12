package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/kaeawc/spectra/internal/rules"
)

func TestLoadRuleCatalogUsesProjectConfig(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "spectra.yml"), []byte(`
rules:
  disabled:
    - app-unsigned
  severity:
    jvm-eol-version: high
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	catalog, err := loadRuleCatalog("", &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if findRule(catalog, "app-unsigned") != nil {
		t.Fatal("app-unsigned was not disabled")
	}
	rule := findRule(catalog, "jvm-eol-version")
	if rule == nil {
		t.Fatal("jvm-eol-version missing")
	}
	if rule.Severity != rules.SeverityHigh {
		t.Fatalf("severity = %q, want high", rule.Severity)
	}
	if stderr.String() != "" {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestLoadRuleCatalogIgnoresMissingDefaultConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	catalog, err := loadRuleCatalog("", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != len(rules.V1Catalog()) {
		t.Fatalf("len(catalog) = %d, want %d", len(catalog), len(rules.V1Catalog()))
	}
}

func TestLoadRuleCatalogErrorsOnMissingExplicitConfig(t *testing.T) {
	_, err := loadRuleCatalog(filepath.Join(t.TempDir(), "missing.yml"), &bytes.Buffer{})
	if err == nil {
		t.Fatal("loadRuleCatalog succeeded, want missing explicit config error")
	}
}

func TestLoadRuleCatalogWarnsOnUnknownRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spectra.yml")
	if err := os.WriteFile(path, []byte(`
rules:
  disabled:
    - no-such-rule
`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if _, err := loadRuleCatalog(path, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); got != "warning: rules override references unknown rule \"no-such-rule\"\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestLoadRuleCatalogLoadsYAMLRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(path, []byte(`
- id: yaml-host
  severity: info
  match: "true"
  message: "host finding"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := loadRuleCatalogWithOptions(ruleCatalogOptions{RulePaths: []string{path}}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	rule := findRule(catalog, "yaml-host")
	if rule == nil {
		t.Fatal("yaml-host missing")
	}
	if rule.Source != path {
		t.Fatalf("source = %q, want %q", rule.Source, path)
	}
}

func TestLoadRuleCatalogRejectsYAMLDuplicateBuiltIn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yml")
	if err := os.WriteFile(path, []byte(`
- id: app-unsigned
  severity: info
  match: "true"
  message: "duplicate"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadRuleCatalogWithOptions(ruleCatalogOptions{RulePaths: []string{path}}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("loadRuleCatalogWithOptions succeeded, want duplicate ID error")
	}
}

func findRule(catalog []rules.Rule, id string) *rules.Rule {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

func TestParsePIDList(t *testing.T) {
	pids, err := parsePIDList(" 10, 20,,30 ")
	if err != nil || len(pids) != 3 || pids[0] != 10 || pids[2] != 30 {
		t.Fatalf("parsePIDList = %v, %v", pids, err)
	}
	if pids, err := parsePIDList(""); err != nil || pids != nil {
		t.Fatalf("empty = %v, %v", pids, err)
	}
	for _, bad := range []string{"abc", "0", "-5"} {
		if _, err := parsePIDList(bad); err == nil {
			t.Errorf("parsePIDList(%q) should fail", bad)
		}
	}
}

func TestRunRulesRejectsThreadDumpWithStoredSnapshot(t *testing.T) {
	if code := runRules([]string{"--snapshot", "snap-x", "--thread-dump-pid", "10"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if code := runRules([]string{"--thread-dump-pid", "nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
