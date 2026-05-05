// Command spectra is the local CLI for Spectra. It dispatches to one of
// several subcommands. The default (no subcommand) inspects the .app
// bundles passed as positional args.
//
//	spectra /Applications/Slack.app             # inspect (default)
//	spectra /Applications/*.app
//	spectra --all                               # inspect every /Applications app
//	spectra --json /Applications/Cursor.app
//	spectra list                                # inspect every /Applications app
//	spectra snapshot                            # capture host + apps snapshot
//	spectra snapshot --json
//	spectra version
//	spectra help
package main

import (
	"fmt"
	"os"
	"strings"
)

var version = "dev"

// subcommand is one entry in the dispatch table.
type subcommand struct {
	name string
	desc string
	run  func(args []string) int
}

// subcommandList returns the full list of subcommands. It's a function
// rather than a package-level var so the help subcommand can reference
// the list without creating an initialization cycle.
func subcommandList() []subcommand {
	return []subcommand{
		{"inspect", "Inspect .app bundles (default; runs when no subcommand given)", runInspect},
		{"list", "Inspect every .app under /Applications", runList},
		{"snapshot", "Capture a structured snapshot of host + installed apps", runSnapshot},
		{"jvm", "List or inspect running JVM processes", runJVM},
		{"toolchain", "Show installed language runtimes and package managers", runToolchain},
		{"network", "Show current network state (routes, DNS, VPN, proxy, listening ports)", runNetwork},
		{"power", "Show current battery and thermal state", runPower},
		{"storage", "Show disk volumes and ~/Library footprint", runStorage},
		{"process", "List running processes sorted by memory (RSS)", runProcess},
		{"diff", "Diff two stored snapshots (alias for snapshot diff)", runSnapshotDiff},
		{"rules", "Evaluate recommendations rules against a snapshot", runRules},
		{"issues", "List, check, or update persisted issues from the recommendations engine", runIssues},
		{"serve", "Run the local daemon (Unix socket JSON-RPC server)", runServe},
		{"status", "Check whether the local daemon is running", runStatus},
		{"metrics", "Show stored process metrics (requires spectra serve)", runMetrics},
		{"install-helper", "Install the privileged helper daemon (requires sudo)", runInstallHelperCmd},
		{"sample", "Collect a user-space CPU sample of a running process", runSample},
		{"cache", "Manage the local blob cache (stats, clear)", runCache},
		{"version", "Print Spectra version and exit", runVersion},
		{"help", "Show this help text", runHelpCmd},
	}
}

func main() {
	initCacheStores()
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch routes args to a subcommand handler. The first non-flag arg
// matching a known subcommand name selects that subcommand; otherwise
// args fall through to `inspect` for backward compatibility with the
// flag-only CLI shape.
func dispatch(args []string) int {
	if len(args) == 0 {
		runHelp(os.Stderr)
		return 2
	}
	first := args[0]
	if !strings.HasPrefix(first, "-") {
		for _, sc := range subcommandList() {
			if first == sc.name {
				return sc.run(args[1:])
			}
		}
	}
	// No subcommand matched — default to inspect with the full arg list.
	return runInspect(args)
}

func runVersion(_ []string) int {
	fmt.Println(version)
	return 0
}

func runList(args []string) int {
	return runInspect(listInspectArgs(args))
}

func listInspectArgs(args []string) []string {
	next := make([]string, 0, len(args)+1)
	next = append(next, "--all")
	next = append(next, args...)
	return next
}

func runHelpCmd(_ []string) int {
	runHelp(os.Stdout)
	return 0
}

func runHelp(w *os.File) {
	fmt.Fprintln(w, "Spectra — macOS app diagnostics, JVM-aware remote debugging portal.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage: spectra <subcommand> [flags] [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	for _, sc := range subcommandList() {
		fmt.Fprintf(w, "  %-10s %s\n", sc.name, sc.desc)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flag-only invocations route to `inspect`:")
	fmt.Fprintln(w, "  spectra /Applications/Slack.app")
	fmt.Fprintln(w, "  spectra --all -v")
	fmt.Fprintln(w, "  spectra list -v")
	fmt.Fprintln(w, "  spectra --json /Applications/Cursor.app")
}
