package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/sourcemap"
)

type symResult struct {
	Input  string `json:"input"`
	Mapped bool   `json:"mapped"`
	Source string `json:"source,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Name   string `json:"name,omitempty"`
}

func runWebSymbolicate(args []string, stdout, stderr io.Writer, readFile func(string) ([]byte, error)) int {
	fs := flag.NewFlagSet("web symbolicate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: spectra web symbolicate [--json] <map.js.map> <line>:<col> [<line>:<col> ...]")
		fmt.Fprintln(stderr, "Resolve minified generated positions to their original source locations.")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return 2
	}
	data, err := readFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "read %s: %v\n", fs.Arg(0), err)
		return 1
	}
	sm, err := sourcemap.Parse(data)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	results := make([]symResult, 0, fs.NArg()-1)
	for _, posArg := range fs.Args()[1:] {
		r, err := symbolicateOne(sm, posArg)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 2
		}
		results = append(results, r)
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	for _, r := range results {
		fmt.Fprintln(stdout, formatSym(r))
	}
	return 0
}

func symbolicateOne(sm *sourcemap.SourceMap, posArg string) (symResult, error) {
	line, col, err := parsePosition(posArg)
	if err != nil {
		return symResult{}, err
	}
	r := symResult{Input: posArg}
	if p, ok := sm.Lookup(line, col); ok {
		r.Mapped = true
		r.Source, r.Line, r.Column, r.Name = p.Source, p.Line, p.Column, p.Name
	}
	return r, nil
}

func parsePosition(s string) (line, col int, err error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad position %q, want line:col", s)
	}
	line, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("bad line in %q: %w", s, err)
	}
	col, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("bad column in %q: %w", s, err)
	}
	return line, col, nil
}

func formatSym(r symResult) string {
	if !r.Mapped {
		return r.Input + " -> no mapping"
	}
	out := fmt.Sprintf("%s -> %s:%d:%d", r.Input, r.Source, r.Line, r.Column)
	if r.Name != "" {
		out += " (" + r.Name + ")"
	}
	return out
}
