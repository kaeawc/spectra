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
	"time"
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
		{"playbook", "Show diagnostic playbooks and command plans", runPlaybook},
		{"diff", "Diff two stored snapshots (alias for snapshot diff)", runSnapshotDiff},
		{"rules", "Evaluate recommendations rules against a snapshot", runRules},
		{"issues", "List, check, or update persisted issues from the recommendations engine", runIssues},
		{"baseline", "Manage baseline snapshots (list, drop)", runSnapshotBaseline},
		{"serve", "Run the daemon (Unix socket, TCP, or tsnet JSON-RPC server)", runServe},
		{"connect", "Call a Spectra daemon over Unix socket, TCP, or MagicDNS", runConnect},
		{"fan", "Run one daemon RPC call against multiple targets", runFan},
		{"hosts", "List hosts known from stored snapshots", runHosts},
		{"status", "Check whether the local daemon is running", runStatus},
		{"metrics", "Show stored process metrics (requires spectra serve)", runMetrics},
		{"install-helper", "Install the privileged helper daemon (requires sudo)", runInstallHelperCmd},
		{"install-daemon", "Install the user LaunchAgent for spectra serve", runInstallDaemonCmd},
		{"sample", "Collect a user-space CPU sample of a running process", runSample},
		{"cache", "Manage the local blob cache (stats, clear)", runCache},
		{"version", "Print Spectra version and exit", runVersion},
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
	if remote, ok, err := parseGlobalRemoteArgs(args); ok {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		return runRemoteCommand(remote)
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

type globalRemoteArgs struct {
	target  string
	timeout time.Duration
	args    []string
}

func parseGlobalRemoteArgs(args []string) (globalRemoteArgs, bool, error) {
	out := globalRemoteArgs{timeout: 3 * time.Second}
	restStart := 0
	for restStart < len(args) {
		arg := args[restStart]
		switch {
		case arg == "--":
			restStart++
			goto done
		case arg == "--remote" || arg == "--target" || arg == "--rpc-target":
			if restStart+1 >= len(args) {
				return out, true, fmt.Errorf("%s requires a target", arg)
			}
			out.target = args[restStart+1]
			restStart += 2
		case strings.HasPrefix(arg, "--remote="):
			out.target = strings.TrimPrefix(arg, "--remote=")
			restStart++
		case strings.HasPrefix(arg, "--target="):
			out.target = strings.TrimPrefix(arg, "--target=")
			restStart++
		case strings.HasPrefix(arg, "--rpc-target="):
			out.target = strings.TrimPrefix(arg, "--rpc-target=")
			restStart++
		case arg == "--timeout":
			if restStart+1 >= len(args) {
				return out, true, fmt.Errorf("%s requires a duration", arg)
			}
			timeout, err := time.ParseDuration(args[restStart+1])
			if err != nil {
				return out, true, fmt.Errorf("invalid --timeout: %w", err)
			}
			out.timeout = timeout
			restStart += 2
		case strings.HasPrefix(arg, "--timeout="):
			timeout, err := time.ParseDuration(strings.TrimPrefix(arg, "--timeout="))
			if err != nil {
				return out, true, fmt.Errorf("invalid --timeout: %w", err)
			}
			out.timeout = timeout
			restStart++
		default:
			goto done
		}
	}
done:
	if out.target == "" {
		return out, false, nil
	}
	out.args = normalizeRemoteCommandArgs(args[restStart:])
	return out, true, nil
}

func normalizeRemoteCommandArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	if strings.HasSuffix(args[0], ".app") || strings.HasPrefix(args[0], "/") {
		next := make([]string, 0, len(args)+1)
		next = append(next, "inspect")
		next = append(next, args...)
		return next
	}
	return args
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
	fmt.Fprintln(w, "       spectra --remote <target> <subcommand> [args]")
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
	fmt.Fprintln(w, "  spectra --remote work-mac jvm")
	fmt.Fprintln(w, "  spectra --remote local inspect /Applications/Slack.app")
}
