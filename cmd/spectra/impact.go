package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kaeawc/spectra/internal/sysinfo"
)

// impactDeps injects env, assertion collection, and live rusage sampling.
type impactDeps struct {
	getenv     func(string) string
	assertions func() []sysinfo.PowerAssertion
	power      powerDeps
}

// defaultImpactTop bounds the live table; a host has hundreds of pids.
const defaultImpactTop = 10

func runImpact(args []string) int {
	return runImpactWithIO(args, os.Stdin, os.Stdout, os.Stderr, impactDeps{
		getenv:     os.Getenv,
		assertions: defaultAssertions,
		power:      defaultPowerDeps(),
	})
}

func defaultAssertions() []sysinfo.PowerAssertion {
	return sysinfo.CollectAssertions(sysinfo.DefaultRunner)
}

func runImpactWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer, deps impactDeps) int {
	fs := flag.NewFlagSet("spectra impact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human table")
	fromJSON := fs.String("from-json", "", "Score a JSON array of ImpactInput records (or '-' for stdin) instead of sampling live")
	interval := fs.Duration("interval", time.Second, "Live sampling window (ignored with --from-json)")
	top := fs.Int("top", 0, "Show the N highest-impact pids (default 10 live, all rows with --from-json)")
	formula := fs.Bool("formula", false, "Print the impact formula and active weights, then exit")

	weights, err := sysinfo.LoadWeights(deps.getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: spectra impact [--json] [--interval d] [--top n] [--from-json file|-] [--formula]")
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

	inputs, limit, code := collectImpactInputs(stdin, stderr, deps, *fromJSON, *interval, *top)
	if code != 0 {
		return code
	}

	rows := sysinfo.ScoreImpacts(inputs, deps.assertions(), weights)
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rows)
		return 0
	}

	printImpactTable(stdout, rows)
	return 0
}

// collectImpactInputs reads --from-json when given and otherwise samples
// live, returning the row limit to apply and a non-zero exit code on failure.
func collectImpactInputs(stdin io.Reader, stderr io.Writer, deps impactDeps, fromJSON string, interval time.Duration, top int) ([]sysinfo.ImpactInput, int, int) {
	if interval <= 0 || top < 0 {
		fmt.Fprintln(stderr, "impact: --interval must be > 0 and --top must be >= 0")
		return nil, 0, 2
	}
	if fromJSON != "" {
		inputs, code := fileImpactInputs(stderr, fromJSON, stdin)
		return inputs, top, code
	}
	if top == 0 {
		top = defaultImpactTop
	}
	inputs, code := liveImpactInputs(stderr, deps.power, interval)
	return inputs, top, code
}

// liveImpactInputs samples per-pid rusage over interval, the same source
// `spectra power --top` and the MCP power impact op use.
func liveImpactInputs(stderr io.Writer, deps powerDeps, interval time.Duration) ([]sysinfo.ImpactInput, int) {
	deltas, code, ok := collectRusageDeltas(stderr, deps, interval)
	if !ok {
		return nil, code
	}
	inputs := make([]sysinfo.ImpactInput, 0, len(deltas))
	for _, d := range deltas {
		inputs = append(inputs, sysinfo.FromRusage(d))
	}
	return inputs, 0
}

func fileImpactInputs(stderr io.Writer, path string, stdin io.Reader) ([]sysinfo.ImpactInput, int) {
	inputs, err := readImpactInputs(path, stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return inputs, 0
}

func readImpactInputs(path string, stdin io.Reader) ([]sysinfo.ImpactInput, error) {
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
