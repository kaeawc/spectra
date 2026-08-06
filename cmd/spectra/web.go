package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/asar"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/electronfuse"
)

func runWeb(args []string) int {
	return runWebWithIO(args, os.Stdout, os.Stderr, detect.DetectWith)
}

type detectFunc func(string, detect.Options) (detect.Result, error)

func runWebWithIO(args []string, stdout, stderr io.Writer, inspect detectFunc) int {
	sub := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	switch sub {
	case "asar-diff":
		return runAsarDiff(rest, stdout, stderr, inspect)
	case "fuses":
		return runWebFuses(rest, stdout, stderr, os.ReadFile)
	case "processes":
		return runWebProcesses(rest, stdout, stderr, defaultProcessCollector)
	default:
		fmt.Fprintf(stderr, "unknown web subcommand %q (want: asar-diff, fuses, processes)\n", sub)
		return 2
	}
}

type webFusesOutput struct {
	App      string                 `json:"app"`
	Config   *electronfuse.Config   `json:"config"`
	Findings []electronfuse.Finding `json:"findings,omitempty"`
}

func runWebFuses(args []string, stdout, stderr io.Writer, readFile func(string) ([]byte, error)) int {
	fs := flag.NewFlagSet("web fuses", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra web fuses [--json] <app.app>")
		fmt.Fprintln(stderr, "Audit an Electron app's build-time security fuses (RunAsNode, inspect args, asar integrity).")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	app := fs.Arg(0)
	bin := filepath.Join(app, "Contents", "Frameworks", "Electron Framework.framework", "Electron Framework")
	data, err := readFile(bin)
	if err != nil {
		fmt.Fprintf(stderr, "%s: not an Electron app or no Electron Framework binary: %v\n", app, err)
		return 1
	}
	cfg, err := electronfuse.Parse(data)
	if err != nil {
		if errors.Is(err, electronfuse.ErrNoSentinel) {
			fmt.Fprintf(stderr, "%s: no Electron fuse wire found in the framework binary\n", app)
			return 1
		}
		fmt.Fprintf(stderr, "%s: %v\n", app, err)
		return 1
	}
	name := strings.TrimSuffix(filepath.Base(app), ".app")
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(webFusesOutput{App: name, Config: cfg, Findings: cfg.Audit()}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderWebFuses(stdout, name, cfg)
	return 0
}

func renderWebFuses(w io.Writer, app string, cfg *electronfuse.Config) {
	fmt.Fprintf(w, "%s — Electron fuses (schema v%d)\n", app, cfg.Version)
	for _, f := range cfg.Fuses {
		fmt.Fprintf(w, "  %-40s %s\n", f.Name, f.Status)
	}
	findings := cfg.Audit()
	if len(findings) == 0 {
		fmt.Fprintln(w, "\nno dangerous fuse configuration detected")
		return
	}
	fmt.Fprintln(w, "")
	for _, fd := range findings {
		fmt.Fprintf(w, "  [%s] %s: %s\n", fd.Severity, fd.Fuse, fd.Message)
	}
}

// asarPayload is the version-independent inventory of one Electron app.
type asarPayload struct {
	App       string
	Archive   *asar.Archive
	NPM       []string
	Native    []string
	Endpoints []string
}

type webAsarDiff struct {
	AppA             string    `json:"app_a"`
	AppB             string    `json:"app_b"`
	Files            asar.Diff `json:"files"`
	NPMAdded         []string  `json:"npm_added,omitempty"`
	NPMRemoved       []string  `json:"npm_removed,omitempty"`
	NativeAdded      []string  `json:"native_added,omitempty"`
	NativeRemoved    []string  `json:"native_removed,omitempty"`
	EndpointsAdded   []string  `json:"endpoints_added,omitempty"`
	EndpointsRemoved []string  `json:"endpoints_removed,omitempty"`
}

func runAsarDiff(args []string, stdout, stderr io.Writer, inspect detectFunc) int {
	fs := flag.NewFlagSet("web asar-diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra web asar-diff [--json] <oldApp.app> <newApp.app>")
		fmt.Fprintln(stderr, "Diff two Electron apps' app.asar payloads: changed files and new npm/native/endpoint capabilities.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	a, err := buildAsarPayload(fs.Arg(0), inspect)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	b, err := buildAsarPayload(fs.Arg(1), inspect)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	diff := computeWebAsarDiff(a, b)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(diff); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	renderWebAsarDiff(stdout, diff)
	return 0
}

func buildAsarPayload(appPath string, inspect detectFunc) (asarPayload, error) {
	asarPath := filepath.Join(appPath, "Contents", "Resources", "app.asar")
	arch, err := asar.ParseFile(asarPath)
	if err != nil {
		return asarPayload{}, fmt.Errorf("%s: %w", appPath, err)
	}
	res, err := inspect(appPath, detect.Options{ScanNetwork: true})
	if err != nil {
		return asarPayload{}, fmt.Errorf("inspect %s: %w", appPath, err)
	}
	p := asarPayload{
		App:       strings.TrimSuffix(filepath.Base(appPath), ".app"),
		Archive:   arch,
		Native:    arch.NativeModulePaths(),
		Endpoints: res.NetworkEndpoints,
	}
	if res.Dependencies != nil {
		p.NPM = res.Dependencies.NPMPackages
	}
	return p, nil
}

func computeWebAsarDiff(a, b asarPayload) webAsarDiff {
	return webAsarDiff{
		AppA:             a.App,
		AppB:             b.App,
		Files:            asar.DiffArchives(a.Archive, b.Archive),
		NPMAdded:         setSubtract(b.NPM, a.NPM),
		NPMRemoved:       setSubtract(a.NPM, b.NPM),
		NativeAdded:      setSubtract(b.Native, a.Native),
		NativeRemoved:    setSubtract(a.Native, b.Native),
		EndpointsAdded:   setSubtract(b.Endpoints, a.Endpoints),
		EndpointsRemoved: setSubtract(a.Endpoints, b.Endpoints),
	}
}

// setSubtract returns the items in a that are not in b, sorted and de-duped.
func setSubtract(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, x := range b {
		inB[x] = true
	}
	seen := make(map[string]bool)
	var out []string
	for _, x := range a {
		if inB[x] || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func renderWebAsarDiff(w io.Writer, d webAsarDiff) {
	fmt.Fprintf(w, "asar-diff %s -> %s\n", d.AppA, d.AppB)
	fmt.Fprintf(w, "files: +%d added, -%d removed, ~%d changed\n",
		len(d.Files.Added), len(d.Files.Removed), len(d.Files.Changed))
	renderDriftList(w, "+ npm", d.NPMAdded)
	renderDriftList(w, "- npm", d.NPMRemoved)
	renderDriftList(w, "+ native", d.NativeAdded)
	renderDriftList(w, "- native", d.NativeRemoved)
	renderDriftList(w, "+ endpoint", d.EndpointsAdded)
	renderDriftList(w, "- endpoint", d.EndpointsRemoved)
	if len(d.NPMAdded)+len(d.NativeAdded)+len(d.EndpointsAdded) == 0 && d.Files.Empty() {
		fmt.Fprintln(w, "no changes")
	}
}

func renderDriftList(w io.Writer, label string, items []string) {
	for _, it := range items {
		fmt.Fprintf(w, "  %s %s\n", label, it)
	}
}
