package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/kaeawc/spectra/internal/xpcattr"
)

// whoisSlack is added to --duration for process listing and lsmp.
const whoisSlack = 15 * time.Second

// whoisCapture captures an attribution; tests substitute it.
type whoisCapture func(ctx context.Context, o xpcattr.Options) (xpcattr.Attribution, error)

func runWhois(args []string) int {
	return runWhoisWithIO(args, os.Stdout, os.Stderr, xpcattr.Capture)
}

func runWhoisWithIO(args []string, stdout, stderr io.Writer, capture whoisCapture) int {
	fs := flag.NewFlagSet("spectra whois", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	sudo := fs.Bool("sudo", false, "Run the lsmp fallback under sudo")
	duration := fs.Duration("duration", 3*time.Second, "Sample duration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: spectra whois [--json] [--sudo] [--duration 3s] <pid>")
		return 2
	}
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil || pid <= 0 {
		fmt.Fprintf(stderr, "invalid PID %q\n", fs.Arg(0))
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration+whoisSlack)
	defer cancel()
	a, err := capture(ctx, xpcattr.Options{PID: pid, Duration: *duration, Sudo: *sudo})
	if err != nil {
		fmt.Fprintf(stderr, "whois: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(a); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	printWhois(stdout, a)
	return 0
}

func printWhois(w io.Writer, a xpcattr.Attribution) {
	if len(a.Clients) == 0 {
		fmt.Fprintf(w, "No XPC/Mach clients found for pid %d (no NSXPCConnection queue PIDs in the sample; lsmp may need --sudo).\n", a.PID)
		return
	}
	fmt.Fprintln(w, a.Summary())
	unit := "samples"
	if a.Source == xpcattr.SourceLsmp {
		unit = "ports"
	}
	fmt.Fprintf(w, "Clients (by %s, source: %s):\n", unit, a.Source)
	for _, c := range a.Clients {
		name := c.Name
		if name == "" {
			name = "unknown"
		}
		fmt.Fprintf(w, "  %5.1f%%  %s (%d)  %d %s\n", c.Share*100, name, c.PID, c.Weight, unit)
		if c.CommandLine != "" {
			fmt.Fprintf(w, "           cmd: %s\n", c.CommandLine)
		}
		for _, p := range c.Chain {
			fmt.Fprintf(w, "           <- %s (%d)\n", p.Name, p.PID)
		}
	}
}
