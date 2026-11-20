package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/uninstall"
)

const uninstallUsage = `Usage:
  spectra uninstall plan <path/to/App.app | bundle id> [-json]
  spectra uninstall apply --plan <plan.json> [-json] [--dry-run] [--quit]
                          [--include-low-confidence] [--data-only] [--leftovers]

plan is read-only: it lists the bundle and every file tied to it by bundle id,
declared helper ids, app-group entitlements, launchd jobs or pkgutil receipts,
with size, owner and whether you can move it. Save it with -json.

apply acts only on the items in a plan file. It re-checks each item, skips any
that drifted or are gone, refuses while the app runs (--quit sends SIGTERM and
waits up to 10s), then boots out your own launch agents, and moves the bundle
and then your own items into a new folder in the Trash. It never deletes and
never touches root-owned, shared or receipt-installed items: those are printed
with a command you can run yourself.

  --data-only  move the app's data even though the bundle cannot be moved
               (root-owned, or a move that fails); without it apply stops
  --leftovers  the bundle is gone: move only exact <location>/<bundle id>
               folders, never for com.apple.* ids or an installed app's id

Exit codes: 0 ok, 1 error or refused, 2 usage, 3 apply left items needing root
or failed to move some.
`

// uninstallOptions is the seam tests replace with a fixture tree.
var uninstallOptions = uninstall.DefaultOptions

func runUninstall(args []string) int {
	return runUninstallWithIO(args, os.Stdout, os.Stderr, uninstallOptions)
}

func runUninstallWithIO(args []string, stdout, stderr io.Writer, opts func() uninstall.Options) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, uninstallUsage)
		return 2
	}
	switch args[0] {
	case "plan":
		return runUninstallPlan(args[1:], stdout, stderr, opts())
	case "apply":
		return runUninstallApply(args[1:], stdout, stderr, opts())
	case "-h", "--help", "help":
		fmt.Fprint(stdout, uninstallUsage)
		return 0
	}
	fmt.Fprintf(stderr, "spectra uninstall: unknown subcommand %q\n%s", args[0], uninstallUsage)
	return 2
}

// parseInterspersed lets flags follow the positional argument.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func runUninstallPlan(args []string, stdout, stderr io.Writer, opts uninstall.Options) int {
	fs := flag.NewFlagSet("spectra uninstall plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, uninstallUsage) }
	asJSON := fs.Bool("json", false, "Emit the plan as JSON (the input for apply)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, "spectra uninstall plan: expected one app path or bundle id")
		return 2
	}
	plan, err := uninstall.BuildPlan(context.Background(), opts, pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "spectra uninstall plan: %v\n", err)
		return 1
	}
	if *asJSON {
		err = writeJSON(stdout, plan)
	} else {
		err = writeUninstallPlan(stdout, plan)
	}
	if err != nil {
		fmt.Fprintf(stderr, "spectra uninstall plan: write output: %v\n", err)
		return 1
	}
	return 0
}

func runUninstallApply(args []string, stdout, stderr io.Writer, opts uninstall.Options) int {
	fs := flag.NewFlagSet("spectra uninstall apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, uninstallUsage) }
	planPath := fs.String("plan", "", "Plan file written by `spectra uninstall plan -json`")
	asJSON := fs.Bool("json", false, "Emit the result as JSON")
	dry := fs.Bool("dry-run", false, "Print what would happen and change nothing")
	quit := fs.Bool("quit", false, "Send SIGTERM to the app's processes and wait up to 10s")
	low := fs.Bool("include-low-confidence", false, "Also move name-only (low confidence) matches")
	dataOnly := fs.Bool("data-only", false, "Move the app's data even when the bundle cannot be moved")
	leftovers := fs.Bool("leftovers", false, "The bundle is gone: move only exact <location>/<bundle id> folders")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 || *planPath == "" {
		fmt.Fprintln(stderr, "spectra uninstall apply: --plan <file> is required and takes no positional arguments")
		return 2
	}
	plan, err := readUninstallPlan(*planPath)
	if err != nil {
		fmt.Fprintf(stderr, "spectra uninstall apply: %v\n", err)
		return 1
	}
	res, applyErr := uninstall.Apply(context.Background(), opts, plan,
		uninstall.ApplyOptions{DryRun: *dry, Quit: *quit, IncludeLowConfidence: *low, DataOnly: *dataOnly, Leftovers: *leftovers})
	if *asJSON {
		err = writeJSON(stdout, applyJSON{Result: res, Error: errString(applyErr)})
	} else {
		err = writeUninstallResult(stdout, res)
	}
	if err != nil {
		fmt.Fprintf(stderr, "spectra uninstall apply: write output: %v\n", err)
		return 1
	}
	if applyErr != nil {
		fmt.Fprintf(stderr, "spectra uninstall apply: %v\n", applyErr)
		return 1
	}
	if len(res.NeedsRoot) > 0 || moveFailures(res) > 0 {
		return 3
	}
	return 0
}

type applyJSON struct {
	uninstall.Result
	Error string `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func moveFailures(res uninstall.Result) int {
	n := 0
	for _, s := range res.Skipped {
		if strings.HasPrefix(s.Reason, "move failed:") {
			n++
		}
	}
	return n
}

func readUninstallPlan(path string) (uninstall.Plan, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return uninstall.Plan{}, fmt.Errorf("read plan: %w", err)
	}
	var plan uninstall.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return uninstall.Plan{}, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if plan.Schema != uninstall.PlanSchema {
		return uninstall.Plan{}, errors.New("not an uninstall plan file (schema " + plan.Schema + ")")
	}
	return plan, nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeUninstallPlan(w io.Writer, p uninstall.Plan) error {
	ew := &errWriter{w: w}
	fmt.Fprintf(ew, "%s (%s) %s\n%s\n", p.App.Name, p.App.BundleID, p.App.Version, p.App.Path)
	if p.App.TeamID != "" {
		fmt.Fprintf(ew, "team id %s\n", p.App.TeamID)
	}
	for _, pr := range p.Processes {
		fmt.Fprintf(ew, "running  pid %d  %s\n", pr.PID, pr.Name)
	}
	for _, l := range p.LaunchdLabels {
		fmt.Fprintf(ew, "launchd  %s\n", l)
	}
	sections := []struct {
		action uninstall.Action
		title  string
	}{
		{uninstall.ActionTrash, "Moved to the Trash by apply"},
		{uninstall.ActionNeedsRoot, "Needs root (apply prints the command, never runs it)"},
		{uninstall.ActionReportOnly, "Reported only (shared, receipt-installed or low confidence)"},
	}
	for _, s := range sections {
		writePlanSection(ew, p.Items, s.action, s.title)
	}
	fmt.Fprintf(ew, "\nSave with: spectra uninstall plan -json %s > plan.json\nThen:      spectra uninstall apply --plan plan.json --dry-run\n", shellArg(p.App.Path))
	return ew.err
}

func writePlanSection(w io.Writer, items []uninstall.Item, action uninstall.Action, title string) {
	var total uint64
	n := 0
	for _, it := range items {
		if it.Action == action {
			n++
			total += it.SizeBytes
		}
	}
	if n == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s: %d items, %s\n", title, n, humanSizeUint(total))
	for _, it := range items {
		if it.Action != action {
			continue
		}
		fmt.Fprintf(w, "  %-17s %9s  %s  [%s]\n", it.Kind, humanSizeUint(it.SizeBytes), it.Path, it.Reason)
		if it.Note != "" {
			fmt.Fprintf(w, "      %s\n", it.Note)
		}
		if it.Command != "" {
			fmt.Fprintf(w, "      %s\n", it.Command)
		}
	}
}

func writeUninstallResult(w io.Writer, r uninstall.Result) error {
	ew := &errWriter{w: w}
	verb := "moved"
	if r.DryRun {
		verb = "would move"
		fmt.Fprintln(ew, "dry run: nothing was changed")
	}
	for _, p := range r.Running {
		fmt.Fprintf(ew, "running   pid %d  %s\n", p.PID, p.Name)
	}
	for _, p := range r.Terminated {
		fmt.Fprintf(ew, "SIGTERM   pid %d  %s\n", p.PID, p.Name)
	}
	if r.Leftovers {
		fmt.Fprintln(ew, "leftovers: the bundle is gone; only exact bundle-id folders are acted on")
	}
	for _, b := range r.Bootouts {
		fmt.Fprintf(ew, "bootout   %s %s\n", b.Target, b.Error)
	}
	for _, p := range r.Relaunched {
		fmt.Fprintf(ew, "relaunched pid %d  %s\n", p.PID, p.Name)
	}
	if r.TrashDir != "" {
		fmt.Fprintf(ew, "trash folder %s\n", r.TrashDir)
	}
	for _, m := range r.Moved {
		fmt.Fprintf(ew, "%s %s -> %s\n", verb, m.Path, m.To)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(ew, "skipped   %s (%s)\n", s.Path, s.Reason)
	}
	for _, n := range r.NeedsRoot {
		cmd := n.Command
		if cmd == "" {
			cmd = "(covered by the command above)"
		}
		fmt.Fprintf(ew, "needs root %s\n    %s\n", n.Path, cmd)
	}
	return ew.err
}

func shellArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
