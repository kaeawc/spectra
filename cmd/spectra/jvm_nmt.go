package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/kaeawc/spectra/internal/jvm"
)

const jvmNMTUsage = `usage: spectra jvm nmt [--json] [--top N] <pid>
       spectra jvm nmt detail [--json] [--top N] <pid>
       spectra jvm nmt baseline <pid>
       spectra jvm nmt diff [--json] [--top N] <pid>
       spectra jvm nmt compare [--json] [--top N] <before-file> <after-file>`

// nmtCompareReport is the JSON shape emitted by `jvm nmt compare --json`.
type nmtCompareReport struct {
	Summary jvm.NMTDiff            `json:"summary"`
	Sites   []jvm.NMTCallSiteDelta `json:"sites,omitempty"`
}

// nmtEnv carries the injectable seams for the `jvm nmt` subcommands.
type nmtEnv struct {
	stdout, stderr io.Writer
	run            jvm.CmdRunner
	readFile       func(string) ([]byte, error)
}

func runJVMNMT(args []string) int {
	return runJVMNMTWith(args, nmtEnv{stdout: os.Stdout, stderr: os.Stderr, readFile: os.ReadFile})
}

func runJVMNMTWith(args []string, env nmtEnv) int {
	if len(args) > 0 {
		switch args[0] {
		case "detail":
			return runNMTDetail(args[1:], env)
		case "baseline":
			return runNMTBaseline(args[1:], env)
		case "diff":
			return runNMTLiveDiff(args[1:], env)
		case "compare":
			return runNMTCompare(args[1:], env)
		}
	}
	return runNMTSummary(args, env)
}

// parseNMTArgs parses the shared flags and validates the positional count.
func parseNMTArgs(name string, args []string, nargs int, env nmtEnv) (*flag.FlagSet, bool, int, bool) {
	fs := flag.NewFlagSet("spectra jvm nmt "+name, flag.ContinueOnError)
	fs.SetOutput(env.stderr)
	asJSON := fs.Bool("json", false, "Emit JSON")
	top := fs.Int("top", 20, "Number of call sites / categories to print")
	if err := fs.Parse(args); err != nil {
		return nil, false, 0, false
	}
	if fs.NArg() != nargs {
		fmt.Fprintln(env.stderr, jvmNMTUsage)
		return nil, false, 0, false
	}
	return fs, *asJSON, *top, true
}

func nmtPID(fs *flag.FlagSet, env nmtEnv) (int, bool) {
	pid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(env.stderr, "invalid PID %q\n", fs.Arg(0))
		return 0, false
	}
	return pid, true
}

// nmtTarget parses args for a single-PID subcommand.
func nmtTarget(name string, args []string, env nmtEnv) (pid int, asJSON bool, top, code int) {
	fs, asJSON, top, ok := parseNMTArgs(name, args, 1, env)
	if !ok {
		return 0, false, 0, 2
	}
	pid, ok = nmtPID(fs, env)
	if !ok {
		return 0, false, 0, 2
	}
	return pid, asJSON, top, 0
}

// nmtFailed prints err and returns the failure exit code.
func nmtFailed(env nmtEnv, err error) int {
	fmt.Fprintln(env.stderr, err)
	return 1
}

func runNMTSummary(args []string, env nmtEnv) int {
	pid, asJSON, top, code := nmtTarget("summary", args, env)
	if code != 0 {
		return code
	}
	b, err := jvm.ReadNMTSummary(pid, env.run)
	if err != nil {
		return nmtFailed(env, err)
	}
	if asJSON {
		return writeIndentedJSON(env.stdout, b)
	}
	printNMTSummary(env.stdout, b, top)
	return 0
}

func runNMTDetail(args []string, env nmtEnv) int {
	pid, asJSON, top, code := nmtTarget("detail", args, env)
	if code != 0 {
		return code
	}
	d, err := jvm.ReadNMTDetail(pid, env.run)
	if err != nil {
		return nmtFailed(env, err)
	}
	if asJSON {
		return writeIndentedJSON(env.stdout, d)
	}
	printNMTSummary(env.stdout, d.Summary, top)
	printNMTSites(env.stdout, d.Sites, top)
	return 0
}

func runNMTBaseline(args []string, env nmtEnv) int {
	pid, _, _, code := nmtTarget("baseline", args, env)
	if code != 0 {
		return code
	}
	out, err := jvm.NativeMemory(pid, "baseline", env.run)
	if err != nil {
		fmt.Fprintf(env.stderr, "nmt baseline failed for PID %d: %v\n", pid, err)
		return 1
	}
	if strings.Contains(strings.ToLower(string(out)), "not enabled") {
		return nmtFailed(env, jvm.ErrNMTDisabledHint)
	}
	fmt.Fprintln(env.stdout, "NMT baseline recorded; run `spectra jvm nmt diff <pid>` later to see growth.")
	return 0
}

func runNMTLiveDiff(args []string, env nmtEnv) int {
	pid, asJSON, top, code := nmtTarget("diff", args, env)
	if code != 0 {
		return code
	}
	d, err := jvm.ReadNMTDiff(pid, env.run)
	if err != nil {
		return nmtFailed(env, err)
	}
	if asJSON {
		return writeIndentedJSON(env.stdout, d)
	}
	printNMTDiff(env.stdout, d, top)
	return 0
}

// runNMTCompare diffs two saved `VM.native_memory summary` or `detail`
// captures; when both are detail captures, call-site growth is included.
func runNMTCompare(args []string, env nmtEnv) int {
	fs, asJSON, top, ok := parseNMTArgs("compare", args, 2, env)
	if !ok {
		return 2
	}
	var details [2]jvm.NMTDetail
	for i := range details {
		data, err := env.readFile(fs.Arg(i))
		if err != nil {
			fmt.Fprintf(env.stderr, "reading %q: %v\n", fs.Arg(i), err)
			return 1
		}
		if err := checkNMTCaptureTotals(string(data)); err != nil {
			fmt.Fprintf(env.stderr, "%q: %v\n", fs.Arg(i), err)
			return 1
		}
		details[i] = jvm.ParseNMTDetail(string(data))
		if !details[i].Summary.Enabled || len(details[i].Summary.Categories) == 0 {
			fmt.Fprintf(env.stderr, "%q is not a native memory tracking summary or detail capture\n", fs.Arg(i))
			return 1
		}
	}
	report := nmtCompareReport{Summary: jvm.DiffNMTSummaries(details[0].Summary, details[1].Summary)}
	// A summary capture has no call sites, so diffing it against a detail
	// capture would report every site as new growth.
	if len(details[0].Sites) > 0 && len(details[1].Sites) > 0 {
		report.Sites = jvm.DiffNMTCallSites(details[0].Sites, details[1].Sites)
	}
	if asJSON {
		return writeIndentedJSON(env.stdout, report)
	}
	printNMTDiff(env.stdout, report.Summary, top)
	printNMTSiteDeltas(env.stdout, report.Sites, top)
	return 0
}

var (
	nmtCaptureTotalRe = regexp.MustCompile(`Total: reserved=\d+KB, committed=\d+KB`)
	nmtDeltaSuffixRe  = regexp.MustCompile(`=\d+KB [+-]\d+KB`)
)

// checkNMTCaptureTotals rejects `summary.diff` output and other captures whose
// Total line is missing; the summary parser would otherwise report zeros.
// Text that is not NMT output at all falls through to the generic error.
func checkNMTCaptureTotals(text string) error {
	if nmtCaptureTotalRe.MatchString(text) {
		return nil
	}
	if nmtDeltaSuffixRe.MatchString(text) {
		return errors.New("looks like `summary.diff` output; save a plain `VM.native_memory summary` or `detail` capture instead (compare computes the diff itself)")
	}
	if strings.Contains(text, "Native Memory Tracking") {
		return errors.New("no `Total: reserved=..., committed=...` line found; save a complete `VM.native_memory summary` or `detail` capture")
	}
	return nil
}

func printNMTSummary(w io.Writer, b jvm.NMTBreakdown, top int) {
	fmt.Fprintf(w, "Native memory: reserved %d KB, committed %d KB\n", b.TotalReservedKiB, b.TotalCommittedKiB)
	fmt.Fprintf(w, "  %-28s %14s %14s\n", "CATEGORY", "RESERVED KB", "COMMITTED KB")
	for i, c := range b.Categories {
		if i >= top {
			break
		}
		fmt.Fprintf(w, "  %-28s %14d %14d\n", c.Name, c.ReservedKiB, c.CommittedKiB)
	}
}

func printNMTSites(w io.Writer, sites []jvm.NMTCallSite, top int) {
	if len(sites) == 0 {
		fmt.Fprintln(w, "\nNo call sites (restart the JVM with -XX:NativeMemoryTracking=detail).")
		return
	}
	fmt.Fprintf(w, "\nTop call sites by committed size (%d total):\n", len(sites))
	for i, s := range sites {
		if i >= top {
			break
		}
		fmt.Fprintf(w, "  %2d. %-6s %-20s reserved %d KB, committed %d KB\n", i+1, s.Kind, s.Category, s.ReservedKiB, s.CommittedKiB)
		printNMTFrames(w, s.Frames)
	}
}

func printNMTFrames(w io.Writer, frames []string) {
	for _, f := range frames {
		fmt.Fprintf(w, "        %s\n", f)
	}
}

func printNMTDiff(w io.Writer, d jvm.NMTDiff, top int) {
	fmt.Fprintf(w, "Native memory change: reserved %+d KB (%d -> %d), committed %+d KB (%d -> %d)\n",
		d.DeltaReservedKiB, d.BeforeReservedKiB, d.AfterReservedKiB,
		d.DeltaCommittedKiB, d.BeforeCommittedKiB, d.AfterCommittedKiB)
	fmt.Fprintf(w, "  %-28s %16s %16s %14s\n", "CATEGORY", "+COMMITTED KB", "+RESERVED KB", "COMMITTED KB")
	for i, c := range d.Categories {
		if i >= top {
			break
		}
		fmt.Fprintf(w, "  %-28s %+16d %+16d %14d\n", c.Name, c.DeltaCommittedKiB, c.DeltaReservedKiB, c.AfterCommittedKiB)
	}
}

func printNMTSiteDeltas(w io.Writer, sites []jvm.NMTCallSiteDelta, top int) {
	if len(sites) == 0 {
		return
	}
	fmt.Fprintf(w, "\nCall sites by committed growth (%d changed):\n", len(sites))
	for i, s := range sites {
		if i >= top {
			break
		}
		fmt.Fprintf(w, "  %2d. %-6s %-20s committed %+d KB (%d -> %d)\n", i+1, s.Kind, s.Category, s.DeltaCommittedKiB, s.BeforeCommittedKiB, s.AfterCommittedKiB)
		printNMTFrames(w, s.Frames)
	}
}
