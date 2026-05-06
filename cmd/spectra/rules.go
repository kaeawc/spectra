package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/rules"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
)

func runRules(args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "validate":
			return runRulesValidate(args[1:])
		case "list":
			return runRulesList(args[1:])
		case "explain":
			return runRulesExplain(args[1:])
		}
	}
	fs := flag.NewFlagSet("spectra rules", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	snapID := fs.String("snapshot", "", "Evaluate against a stored snapshot by ID (default: take a live snapshot)")
	rulesConfig := fs.String("rules-config", "", "Path to spectra.yml rule overrides (default: ./spectra.yml if present)")
	rulePaths := fs.String("rules", "", "Comma-separated YAML rule files or globs to load")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var snap snapshot.Snapshot
	if *snapID != "" {
		s, err := loadStoredSnapshot(*snapID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rules: %v\n", err)
			return 1
		}
		snap = *s
	} else {
		snap = snapshot.Build(context.Background(), snapshot.Options{
			SpectraVersion: version,
			DetectOpts:     detect.Options{},
		})
	}

	catalog, err := loadRuleCatalogWithOptions(ruleCatalogOptions{
		ConfigPath: *rulesConfig,
		RulePaths:  rules.SplitRulePaths(*rulePaths),
	}, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rules: %v\n", err)
		return 1
	}
	findings := rules.Evaluate(snap, catalog)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(findings)
		return 0
	}

	if len(findings) == 0 {
		fmt.Println("no findings — all rules passed")
		return 0
	}

	printFindings(findings)
	return 0
}

func loadRuleCatalog(configPath string, stderr io.Writer) ([]rules.Rule, error) {
	return loadRuleCatalogWithOptions(ruleCatalogOptions{ConfigPath: configPath}, stderr)
}

type ruleCatalogOptions struct {
	ConfigPath string
	RulePaths  []string
}

func loadRuleCatalogWithOptions(opts ruleCatalogOptions, stderr io.Writer) ([]rules.Rule, error) {
	catalog, err := rules.LoadCatalog(
		rules.BuiltinCatalogSource{},
		rules.YAMLFileCatalogSource{Paths: opts.RulePaths},
	)
	if err != nil {
		return nil, err
	}
	path, explicit := resolveRulesConfigPath(opts.ConfigPath)
	if path == "" {
		return catalog, nil
	}
	overrides, err := rules.LoadOverrides(path)
	if err != nil {
		if explicit {
			return nil, err
		}
		if os.IsNotExist(err) {
			return catalog, nil
		}
		return nil, err
	}
	for _, warning := range rules.OverrideWarnings(overrides, catalog) {
		fmt.Fprintf(stderr, "warning: %s\n", warning)
	}
	return rules.ApplyOverrides(catalog, overrides), nil
}

func runRulesValidate(args []string) int {
	fs := flag.NewFlagSet("spectra rules validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	rulePaths := fs.String("rules", "", "Comma-separated YAML rule files or globs to validate")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths := rules.SplitRulePaths(*rulePaths)
	if len(paths) == 0 {
		paths = rules.SplitRulePaths(strings.Join(fs.Args(), ","))
	}
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra rules validate --rules <rules.yml[,more.yml]>")
		return 2
	}
	if _, err := rules.LoadCatalog(rules.BuiltinCatalogSource{}, rules.YAMLFileCatalogSource{Paths: paths}); err != nil {
		fmt.Fprintf(os.Stderr, "rules validate: %v\n", err)
		return 1
	}
	fmt.Printf("validated %d rule file(s)\n", len(paths))
	return 0
}

func runRulesList(args []string) int {
	fs := flag.NewFlagSet("spectra rules list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	rulesConfig := fs.String("rules-config", "", "Path to spectra.yml rule overrides (default: ./spectra.yml if present)")
	rulePaths := fs.String("rules", "", "Comma-separated YAML rule files or globs to load")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	catalog, err := loadRuleCatalogWithOptions(ruleCatalogOptions{
		ConfigPath: *rulesConfig,
		RulePaths:  rules.SplitRulePaths(*rulePaths),
	}, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rules list: %v\n", err)
		return 1
	}
	summary := rules.SummarizeCatalog(catalog)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}
	fmt.Printf("%-8s  %-30s  %s\n", "SEVERITY", "RULE", "SOURCE")
	fmt.Println(strings.Repeat("-", 80))
	for _, row := range summary {
		fmt.Printf("%-8s  %-30s  %s\n", row.Severity, truncate(row.ID, 30), row.Source)
	}
	fmt.Printf("\n%d rule(s)\n", len(summary))
	return 0
}

func runRulesExplain(args []string) int {
	fs := flag.NewFlagSet("spectra rules explain", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	rulesConfig := fs.String("rules-config", "", "Path to spectra.yml rule overrides (default: ./spectra.yml if present)")
	rulePaths := fs.String("rules", "", "Comma-separated YAML rule files or globs to load")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra rules explain [--rules rules.yml] <rule-id>")
		return 2
	}
	catalog, err := loadRuleCatalogWithOptions(ruleCatalogOptions{
		ConfigPath: *rulesConfig,
		RulePaths:  rules.SplitRulePaths(*rulePaths),
	}, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rules explain: %v\n", err)
		return 1
	}
	id := fs.Arg(0)
	for _, row := range rules.SummarizeCatalog(catalog) {
		if row.ID != id {
			continue
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(row)
			return 0
		}
		fmt.Printf("Rule:     %s\n", row.ID)
		fmt.Printf("Severity: %s\n", row.Severity)
		fmt.Printf("Source:   %s\n", row.Source)
		if row.Message != "" {
			fmt.Printf("Message:  %s\n", row.Message)
		}
		if row.Fix != "" {
			fmt.Printf("Fix:      %s\n", row.Fix)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "rules explain: unknown rule %q\n", id)
	return 1
}

func resolveRulesConfigPath(configPath string) (path string, explicit bool) {
	if configPath != "" {
		return configPath, true
	}
	return "spectra.yml", false
}

func loadStoredSnapshot(id string) (*snapshot.Snapshot, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	raw, err := db.GetSnapshotJSON(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("snapshot %q: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("snapshot %q has no JSON blob (taken before this version)", id)
	}
	var s snapshot.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot %q: %w", id, err)
	}
	return &s, nil
}

func printFindings(findings []rules.Finding) {
	fmt.Printf("%-8s  %-30s  %s\n", "SEVERITY", "RULE", "MESSAGE")
	fmt.Println(strings.Repeat("-", 90))
	for _, f := range findings {
		msg := f.Message
		if f.Subject != "" {
			msg = fmt.Sprintf("[%s] %s", f.Subject, msg)
		}
		fmt.Printf("%-8s  %-30s  %s\n", f.Severity, truncate(f.RuleID, 30), msg)
	}
	fmt.Printf("\n%d finding(s)\n", len(findings))
}
