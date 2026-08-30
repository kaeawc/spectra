package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/kaeawc/spectra/internal/process"
	"github.com/kaeawc/spectra/internal/runtimeattach"
)

// procLookup resolves a PID to the minimal identity runtimeattach classifies.
type procLookup func(pid int) (runtimeattach.Process, bool)

func runRuntime(args []string) int {
	return runRuntimeWithIO(args, os.Stdout, os.Stderr, defaultProcLookup, runtimeattach.DefaultProbes())
}

func runRuntimeWithIO(args []string, stdout, stderr io.Writer, lookup procLookup, probes runtimeattach.Probes) int {
	// Accept an optional leading "attach" verb: `spectra runtime attach <pid>`.
	if len(args) > 0 && args[0] == "attach" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("spectra runtime", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra runtime [attach] [--json] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}

	proc, ok := lookup(pid)
	if !ok {
		fmt.Fprintf(stderr, "no running process with PID %d\n", pid)
		return 1
	}

	result := runtimeattach.Classify(proc, probes)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printRuntimeResult(stdout, result)
	return 0
}

func printRuntimeResult(w io.Writer, r runtimeattach.Result) {
	fmt.Fprintf(w, "pid %d: %s\n", r.PID, r.Command)
	if r.ExecutablePath != "" {
		fmt.Fprintf(w, "  exe:      %s\n", r.ExecutablePath)
	}
	fmt.Fprintf(w, "  runtime:  %s (%s)\n", r.Runtime, r.Evidence)
	fmt.Fprintln(w, "  you can ask it:")
	for _, c := range r.Capabilities {
		fmt.Fprintf(w, "    - %s\n        %s\n", c.Name, c.How)
	}
}

func defaultProcLookup(pid int) (runtimeattach.Process, bool) {
	procs := process.CollectAll(context.Background(), process.CollectOptions{})
	for _, p := range procs {
		if p.PID == pid {
			return runtimeattach.Process{
				PID:            p.PID,
				Command:        p.Command,
				ExecutablePath: p.ExecutablePath,
				CommandLine:    p.FullCommandLine,
			}, true
		}
	}
	return runtimeattach.Process{}, false
}
