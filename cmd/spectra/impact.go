package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

func runImpact(args []string) int {
	return runImpactWithIO(args, os.Stdin, os.Stdout, os.Stderr, os.Getenv, defaultAssertions)
}

func defaultAssertions() []sysinfo.PowerAssertion {
	return sysinfo.CollectAssertions(sysinfo.DefaultRunner)
}

func runImpactWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, collectAssertions func() []sysinfo.PowerAssertion) int {
	fs := flag.NewFlagSet("spectra impact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human table")
	fromJSON := fs.String("from-json", "", "Read a JSON array of ImpactInput records (or '-' for stdin)")
	formula := fs.Bool("formula", false, "Print the impact formula and active weights, then exit")

	weights, err := sysinfo.LoadWeights(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: spectra impact [--json] [--from-json file|-] [--formula]")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
		fmt.Fprintln(stderr)
		fmt.Fprint(stderr, sysinfo.ImpactFormulaHelp(weights))
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *formula {
		fmt.Fprint(stdout, sysinfo.ImpactFormulaHelp(weights))
		return 0
	}

	inputs, err := readImpactInputs(*fromJSON, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	rows := sysinfo.ScoreImpacts(inputs, collectAssertions(), weights)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	printImpactTable(stdout, rows)
	return 0
}

func readImpactInputs(path string, stdin io.Reader) ([]sysinfo.ImpactInput, error) {
	if path == "" {
		return nil, nil
	}
	var src io.Reader
	if path == "-" {
		src = stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		src = f
	}
	var inputs []sysinfo.ImpactInput
	if err := json.NewDecoder(src).Decode(&inputs); err != nil {
		return nil, fmt.Errorf("decode impact inputs: %w", err)
	}
	return inputs, nil
}

func printImpactTable(w io.Writer, rows []sysinfo.ScoredImpact) {
	fmt.Fprintf(w, "%-7s  %-6s  %-6s  %-6s  %-6s  %-5s  %-6s  %s\n",
		"PID", "SCORE", "ENERGY", "WAKE", "GPU", "ASRT", "IO", "COMMAND")
	for _, r := range rows {
		b := r.Breakdown
		fmt.Fprintf(w, "%-7d  %-6.1f  %-6.1f  %-6.1f  %-6.1f  %-5.1f  %-6.1f  %s\n",
			r.Input.PID, b.Total, b.FromEnergy, b.FromWakeups, b.FromGPU, b.FromAssertions, b.FromIO,
			truncate(r.Input.Command, 45))
	}
}
