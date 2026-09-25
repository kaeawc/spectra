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
		{"db", "Discover and inspect databases apps connect to (read-only; postgres, mysql, sqlite, mongodb, redis)", runDB},
		{"toolchain", "Show installed language runtimes and package managers", runToolchain},
		{"network", "Show current network state (routes, DNS, VPN, proxy, listening ports)", runNetwork},
		{"power", "Show current battery and thermal state", runPower},
		{"impact", "Compute the documented per-pid energy impact score from delta records", runImpact},
		{"memory", "Show VM compressor, swap, and pressure state", runMemory},
		{"storage", "Show disk volumes and ~/Library footprint", runStorage},
		{"timemachine", "Show Time Machine status, destinations, and local snapshots", runTimeMachine},
		{"services", "Show launchd jobs and plist schedules", runServices},
		{"logs", "Run bounded unified log queries", runLogs},
		{"updates", "Show macOS install/update log entries", runUpdates},
		{"system", "Show system resource limits and saturation", runSystem},
		{"process", "List running processes sorted by memory (RSS)", runProcess},
		{"playbook", "Show diagnostic playbooks and command plans", runPlaybook},
		{"diff", "Diff two stored snapshots (alias for snapshot diff)", runSnapshotDiff},
		{"rules", "Evaluate recommendations rules against a snapshot", runRules},
		{"issues", "List, check, or update persisted issues from the recommendations engine", runIssues},
		{"baseline", "Manage baseline snapshots (list, drop)", runSnapshotBaseline},
		{"fleet", "Cross-host rollups: which hosts trip a rule, and version-drift matrices", runFleet},
		{"bisect", "Find the snapshot where a rule started firing, and what changed alongside it", runBisect},
		{"reconcile", "Print an advisory plan to make one host's toolchain match another's", runReconcile},
		{"anomalies", "Flag processes deviating from their rolling RSS baseline", runAnomalies},
		{"install-helper", "Install the privileged helper daemon (requires sudo)", runInstallHelperCmd},
		{"schedule", "Schedule periodic snapshot capture via a launchd agent", runSchedule},
		{"sample", "Collect a user-space CPU sample of a running process", runSample},
		{"symbolicate", "Resolve raw stack addresses to symbol + file:line via atos", runSymbolicate},
		{"spindump", "Capture and summarize a per-thread stack report (heaviest stacks)", runSpindump},
		{"vmmap", "Summarize a process's memory composition by region (dirty/resident/swapped)", runVmmap},
		{"vmregions", "Composition by sharing/protection/backing, with RWX (W^X) flagging", runVmregions},
		{"lsmp", "Summarize a process's Mach port table (right counts, leak detection)", runLsmp},
		{"malloc-history", "Attribute allocations to call stacks (requires MallocStackLogging)", runMallocHistory},
		{"flamegraph", "Render a native CPU flamegraph (SVG) from a process sample", runFlamegraph},
		{"hang", "Classify why a process's main thread is hung (lock/I-O/spin/idle)", runHang},
		{"timeline", "Chronological incident timeline: process starts, unified-log errors, and installs", runTimeline},
		{"runtime", "Identify a live PID's language runtime and the diagnostics available for it", runRuntime},
		{"xattr-inspect", "Show quarantine, provenance, where-froms, and AppleDouble state of files", runXattrInspect},
		{"core", "Inspect crashed-process core files and suggest offline analyzers", runCore},
		{"crash", "Audit post-mortem readiness (can this machine produce a debuggable crash?)", runCrash},
		{"web", "Diff an Electron app's app.asar payload between two versions", runWeb},
		{"whatswrong", "Ranked whole-system triage: why is this Mac slow right now?", runWhatswrong},
		{"cache", "Manage the local blob cache (stats, clear)", runCache},
		{"version", "Print Spectra version and exit", runVersion},
		{"capabilities", "Describe the machine-readable interfaces this build supports", runCapabilities},
		{"help", "Show this help text", runHelpCmd},
	}
}

func main() {
	initCacheStores()
	initArtifactRecorder()
	os.Exit(dispatch(os.Args[1:]))
}

// dispatch routes args to a subcommand handler. The first non-flag arg
// matching a known subcommand name selects that subcommand; otherwise
// args fall through to `inspect` for backward compatibility with the
// flag-only CLI shape.
func dispatch(args []string) int {
	args, verbose := stripVerboseFlags(args)
	if verbose {
		enableVerbose()
	}
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
	fmt.Fprintln(w, "Spectra — macOS app diagnostics and JVM-aware debugging.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage: spectra <subcommand> [flags] [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --verbose, --debug   Log enhancement/collection failures to stderr (or set SPECTRA_DEBUG)")
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
