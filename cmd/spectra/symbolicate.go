package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/kaeawc/spectra/internal/symbolicate"
)

func runSymbolicate(args []string) int {
	return runSymbolicateWithIO(args, os.Stdin, os.Stdout, os.Stderr, defaultSymbolicateRunner)
}

func runSymbolicateWithIO(args []string, stdin io.Reader, stdout, stderr io.Writer, runner symbolicate.Runner) int {
	fs := flag.NewFlagSet("spectra symbolicate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binary := fs.String("o", "", "Path to the Mach-O image or its .dSYM (required)")
	load := fs.String("l", "", "Load address of the image, e.g. 0x1049f4000 (required)")
	arch := fs.String("arch", "", "Architecture slice of a universal binary, e.g. arm64")
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *binary == "" || *load == "" {
		fmt.Fprintln(stderr, "usage: spectra symbolicate -o <binary-or-dSYM> -l <load-address> [--arch <arch>] [--json] <addr>...")
		return 2
	}

	addresses := fs.Args()
	if len(addresses) == 0 {
		addresses = readAddresses(stdin)
	}
	if len(addresses) == 0 {
		fmt.Fprintln(stderr, "symbolicate: no addresses given (pass them as arguments or on stdin)")
		return 2
	}

	result, err := symbolicate.Symbolicate(context.Background(), symbolicate.Request{
		Binary:      *binary,
		LoadAddress: *load,
		Arch:        *arch,
		Addresses:   addresses,
	}, runner)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	printSymbolicateResult(stdout, result)
	return 0
}

func printSymbolicateResult(w io.Writer, r symbolicate.Result) {
	fmt.Fprintf(w, "symbolicate %s @ %s", r.Binary, r.LoadAddress)
	if r.Arch != "" {
		fmt.Fprintf(w, " (%s)", r.Arch)
	}
	fmt.Fprintln(w)
	for _, f := range r.Frames {
		if !f.Resolved {
			fmt.Fprintf(w, "  %s  <unresolved>\n", f.Address)
			continue
		}
		line := fmt.Sprintf("  %s  %s", f.Address, f.Symbol)
		if f.Location != "" {
			line += "  (" + f.Location + ")"
		}
		fmt.Fprintln(w, line)
	}
}

func readAddresses(r io.Reader) []string {
	if r == nil {
		return nil
	}
	var addrs []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if t := strings.TrimSpace(scanner.Text()); t != "" {
			addrs = append(addrs, t)
		}
	}
	return addrs
}

func defaultSymbolicateRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
