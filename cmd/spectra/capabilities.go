package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/kaeawc/spectra/internal/capabilities"
)

func runCapabilities(args []string) int {
	fs := flag.NewFlagSet("spectra capabilities", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra capabilities [--json]")
		return 2
	}
	if err := writeCapabilities(os.Stdout, *asJSON, runtime.GOOS, runtime.GOARCH); err != nil {
		fmt.Fprintf(os.Stderr, "encode capabilities: %v\n", err)
		return 1
	}
	return 0
}

// writeCapabilities prints the manifest for goos/goarch; they are parameters
// so the contract cases can record both OSes (contract_cases_test.go).
func writeCapabilities(w io.Writer, asJSON bool, goos, goarch string) error {
	manifest := capabilities.Build(version, goos, goarch)
	if asJSON {
		return json.NewEncoder(w).Encode(manifest)
	}
	fmt.Fprintf(w, "Spectra %s (%s/%s)\n", manifest.SpectraVersion, manifest.OS, manifest.Arch)
	fmt.Fprintf(w, "Capabilities schema: %s v%d\n", manifest.Schema.Name, manifest.Schema.Version)
	fmt.Fprintf(w, "%-16s %-7s %-27s %s\n", "NAME", "OUTPUT", "RESULT SCHEMA", "ARGV")
	for _, iface := range manifest.Interfaces {
		schema := "-"
		if iface.ResultSchema != nil {
			schema = fmt.Sprintf("%s v%d", iface.ResultSchema.Name, iface.ResultSchema.Version)
		}
		fmt.Fprintf(w, "%-16s %-7s %-27s %s\n", iface.Name, iface.Output, schema, strings.Join(iface.Argv, " "))
	}
	return nil
}
