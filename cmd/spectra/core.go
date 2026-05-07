package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/corefile"
)

func runCore(args []string) int {
	if len(args) > 0 && args[0] == "inspect" {
		return runCoreInspect(args[1:])
	}
	fmt.Fprintln(os.Stderr, "usage: spectra core inspect [--json] [--exe <path>] <core-file>")
	return 2
}

func runCoreInspect(args []string) int {
	fs := flag.NewFlagSet("spectra core inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	exePath := fs.String("exe", "", "Executable path for the crashed process")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra core inspect [--json] [--exe <path>] <core-file>")
		return 2
	}

	inspector := corefile.Inspector{Analyzers: []corefile.Analyzer{
		corefile.JVMAnalyzer{},
	}}
	report, err := inspector.Inspect(context.Background(), fs.Arg(0), *exePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	printCoreReport(report)
	return 0
}

func printCoreReport(report corefile.Report) {
	fmt.Printf("core:          %s\n", report.Artifact.Path)
	if report.Artifact.ExecutablePath != "" {
		fmt.Printf("executable:    %s\n", report.Artifact.ExecutablePath)
	}
	fmt.Printf("format:        %s\n", valueOrUnknown(report.Artifact.Format))
	if report.Artifact.Architecture != "" {
		fmt.Printf("architecture:  %s\n", report.Artifact.Architecture)
	}
	fmt.Printf("size:          %d bytes\n", report.Artifact.SizeBytes)
	if report.Runtime != "" {
		fmt.Printf("runtime:       %s\n", report.Runtime)
	}
	if len(report.Analyzers) > 0 {
		fmt.Printf("analyzers:     %s\n", strings.Join(report.Analyzers, ", "))
	}
	for _, obs := range report.Observations {
		fmt.Printf("%s: %s\n", obs.Key, obs.Value)
	}
	if len(report.Commands) == 0 {
		return
	}
	fmt.Println("")
	fmt.Println("Suggested commands:")
	for _, cmd := range report.Commands {
		fmt.Printf("  %s %s\n", cmd.Tool, strings.Join(shellQuoteArgs(cmd.Args), " "))
		if cmd.Purpose != "" {
			fmt.Printf("    %s\n", cmd.Purpose)
		}
	}
}

func shellQuoteArgs(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = shellQuote(arg)
	}
	return out
}

func shellQuote(arg string) string {
	if strings.IndexFunc(arg, func(r rune) bool {
		return r != '/' && r != '.' && r != '-' && r != '_' && r != '=' && r != ':' &&
			r != '<' && r != '>' &&
			(r < '0' || r > '9') &&
			(r < 'A' || r > 'Z') &&
			(r < 'a' || r > 'z')
	}) == -1 {
		return arg
	}
	return strconv.Quote(arg)
}

func valueOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
