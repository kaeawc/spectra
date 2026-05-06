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

func findRule(catalog []rules.Rule, id string) *rules.Rule {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}
