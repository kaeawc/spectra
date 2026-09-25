package main

import (
	"encoding/json"
	"flag"
	"fmt"
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
	manifest := capabilities.Build(version, runtime.GOOS, runtime.GOARCH)
	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(manifest); err != nil {
			fmt.Fprintf(os.Stderr, "encode capabilities: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Printf("Spectra %s (%s/%s)\n", manifest.SpectraVersion, manifest.OS, manifest.Arch)
	fmt.Printf("Capabilities schema: %s v%d\n", manifest.Schema.Name, manifest.Schema.Version)
	fmt.Println("NAME          OUTPUT  RESULT SCHEMA           ARGV")
	for _, iface := range manifest.Interfaces {
		schema := "-"
		if iface.ResultSchema != nil {
			schema = fmt.Sprintf("%s v%d", iface.ResultSchema.Name, iface.ResultSchema.Version)
		}
		fmt.Printf("%-13s %-7s %-23s %s\n", iface.Name, iface.Output, schema, strings.Join(iface.Argv, " "))
	}
	return 0
}
