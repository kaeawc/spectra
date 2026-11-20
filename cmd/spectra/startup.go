package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/startup"
)

const (
	startupExitOK      = 0
	startupExitError   = 1
	startupExitUsage   = 2
	startupExitRefused = 3
	startupTimeout     = 2 * time.Minute
)

const startupUsage = `usage: spectra startup [list] [--json] [--scope user|system|all] [--include-apple]
       spectra startup disable <label> [--scope user] [--json]
       spectra startup enable <label> [--scope user] [--json]

What launches at login or boot, one row per item, merged from:
  launchd plists   ~/Library/LaunchAgents (user), /Library/LaunchAgents and
                   /Library/LaunchDaemons (system); /System/Library with --include-apple
  launchctl        print-disabled overrides and loaded/running state (read-only)
  BTM              Login Items from sfltool dumpbtm, only when run as root
                   (otherwise it asks for administrator authorization)

disable and enable change user-scope launchd items only, with launchctl
disable/bootout and enable/bootstrap. Apple (com.apple.*) and system-scope
items, and labels defined by more than one plist or under /System, are
refused with the command to run yourself; BTM-only login items are changed in
System Settings > General > Login Items.

Exit codes: 0 ok, 1 error (or the re-read state does not match the change),
2 usage, 3 refused.
`

func runStartup(args []string) int {
	return runStartupWithIO(args, os.Stdout, os.Stderr, startup.Options{})
}

func runStartupWithIO(args []string, stdout, stderr io.Writer, opts startup.Options) int {
	verb := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	switch verb {
	case "list":
		return runStartupList(args, stdout, stderr, opts)
	case "disable", "enable":
		return runStartupChange(verb, args, stdout, stderr, opts)
	case "help":
		fmt.Fprint(stdout, startupUsage)
		return startupExitOK
	}
	fmt.Fprintf(stderr, "startup: unknown subcommand %q\n", verb)
	fmt.Fprint(stderr, startupUsage)
	return startupExitUsage
}

func startupFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("spectra startup "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, startupUsage) }
	return fs
}

func runStartupList(args []string, stdout, stderr io.Writer, opts startup.Options) int {
	fs := startupFlags("list", stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	scope := fs.String("scope", "all", "Scope: user, system, or all")
	apple := fs.Bool("include-apple", false, "Also read /System/Library/LaunchAgents and LaunchDaemons")
	if err := fs.Parse(args); err != nil {
		return startupExitUsage
	}
	if fs.NArg() != 0 || !validStartupScope(*scope) {
		fs.Usage()
		return startupExitUsage
	}
	opts.Scope, opts.IncludeApple = *scope, *apple
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	rep, err := startup.List(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "startup: %v\n", err)
		return startupExitError
	}
	if *asJSON {
		return writeStartupJSON(stdout, stderr, rep)
	}
	printStartup(stdout, rep)
	return startupExitOK
}

func validStartupScope(s string) bool {
	return s == "user" || s == "system" || s == "all"
}

func runStartupChange(verb string, args []string, stdout, stderr io.Writer, opts startup.Options) int {
	fs := startupFlags(verb, stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	scope := fs.String("scope", "user", "Scope: only user can be changed")
	if err := fs.Parse(args); err != nil {
		return startupExitUsage
	}
	// Allow flags after the label: spectra startup disable <label> --json.
	if fs.NArg() < 1 {
		fs.Usage()
		return startupExitUsage
	}
	label := fs.Arg(0)
	if err := fs.Parse(fs.Args()[1:]); err != nil || fs.NArg() != 0 || label == "" {
		fs.Usage()
		return startupExitUsage
	}
	do := startup.Disable
	if verb == "enable" {
		do = startup.Enable
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	res, err := do(ctx, opts, label, *scope)
	if *asJSON {
		if code := writeStartupJSON(stdout, stderr, res); code != startupExitOK {
			return code
		}
	} else {
		printStartupChange(stdout, res)
	}
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "startup %s: %v\n", verb, err)
		return startupExitError
	case res.Refused:
		return startupExitRefused
	case res.Mismatch != "":
		fmt.Fprintf(stderr, "startup %s: %s did not take effect: %s\n", verb, label, res.Mismatch)
		return startupExitError
	}
	return startupExitOK
}

func writeStartupJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return startupExitError
	}
	return startupExitOK
}

func printStartup(w io.Writer, rep startup.Report) {
	fmt.Fprintf(w, "=== Startup items (%d) ===\n", len(rep.Items))
	fmt.Fprintf(w, "  %-6s  %-13s  %-44s  %-10s  %-16s  %7s  %s\n", "SCOPE", "KIND", "LABEL", "TRIGGER", "STATE", "PID", "OWNER")
	for _, it := range rep.Items {
		pid := "-"
		if it.Running {
			pid = fmt.Sprint(it.PID)
		}
		fmt.Fprintf(w, "  %-6s  %-13s  %-44s  %-10s  %-16s  %7s  %s\n",
			it.Scope, it.Kind, truncate(it.Label, 44), it.Trigger, startupState(it), pid, startupOwner(it))
		if len(it.DuplicateOf) > 0 {
			fmt.Fprintf(w, "          duplicate label: %s, also in %s\n", it.PlistPath, strings.Join(it.DuplicateOf, ", "))
		}
	}
	fmt.Fprintln(w, "Sources:")
	for _, s := range rep.Sources {
		line := fmt.Sprintf("  %-26s %s", s.Name, s.Status)
		if s.Detail != "" {
			line += ": " + s.Detail
		}
		fmt.Fprintln(w, line)
	}
	for _, e := range rep.Errors {
		fmt.Fprintf(w, "Skipped %s: %s\n", e.Path, e.Error)
	}
}

func startupState(it startup.Item) string {
	state := "enabled"
	if it.Disabled {
		state = "disabled"
	}
	switch {
	case it.Running:
		state += ",running"
	case it.Loaded:
		state += ",loaded"
	}
	return state
}

func startupOwner(it startup.Item) string {
	if it.Owner == nil {
		return "-"
	}
	name := it.Owner.App
	if name == "" {
		name = it.Owner.Developer
	}
	if it.Owner.TeamID != "" {
		name += " (" + it.Owner.TeamID + ")"
	}
	return strings.TrimSpace(name)
}

func printStartupChange(w io.Writer, res startup.ChangeResult) {
	if res.Refused {
		fmt.Fprintf(w, "Not changed: %s\n", res.Message)
		for _, c := range res.Suggested {
			fmt.Fprintf(w, "  %s\n", c)
		}
		return
	}
	for _, c := range res.Commands {
		fmt.Fprintf(w, "ran: %s (exit %d)\n", strings.Join(c.Argv, " "), c.ExitCode)
		if c.Stderr != "" {
			fmt.Fprintf(w, "     %s\n", c.Stderr)
		}
	}
	if res.Message != "" {
		fmt.Fprintf(w, "note: %s\n", res.Message)
	}
	if res.After != nil {
		fmt.Fprintf(w, "%s is now %s\n", res.Label, startupState(*res.After))
	}
}
