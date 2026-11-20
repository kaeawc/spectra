package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kaeawc/spectra/internal/helper"
	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/startup"
)

// launchdHelperClient is the part of helperclient a system-scope change uses.
type launchdHelperClient interface {
	LaunchdSystemPrepare(label, action string) (helper.LaunchdPrepareResult, error)
	LaunchdSystemChange(action, token string) (helper.LaunchdChangeResult, error)
}

// startupSystem holds the seams of a system-scope change.
type startupSystem struct {
	client func() launchdHelperClient
	stdin  io.Reader
	isTTY  func() bool
}

func defaultStartupSystem() startupSystem {
	return startupSystem{
		client: func() launchdHelperClient { return helperclient.New() },
		stdin:  os.Stdin,
		isTTY:  stdinIsTTY,
	}
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

type startupSystemRequest struct {
	verb, label string
	asJSON, yes bool
}

const (
	helperInstallHint = "The privileged helper can make this change after verifying the daemon: sudo spectra install-helper"
	helperUpdateHint  = "The installed privileged helper is too old to make this change; update the helper (spectra install-helper): sudo spectra install-helper"
)

// runStartupSystemChange prepares the change in the helper, shows the plan,
// confirms, then executes it with the single-use token. Running as root
// still goes through the helper, so every change is audited in one place.
func runStartupSystemChange(req startupSystemRequest, stdout, stderr io.Writer, opts startup.Options, sys startupSystem) int {
	c := sys.client()
	prep, err := c.LaunchdSystemPrepare(req.label, req.verb)
	switch {
	case helperclient.IsUnavailable(err):
		return startupSystemFallback(req, stdout, stderr, opts, helperInstallHint)
	case helperclient.IsMethodNotFound(err):
		return startupSystemFallback(req, stdout, stderr, opts, helperUpdateHint)
	case helperclient.IsRateLimited(err):
		fmt.Fprintf(stderr, "startup %s: too many changes; try again later (%v)\n", req.verb, err)
		return startupExitError
	case err != nil:
		fmt.Fprintf(stderr, "startup %s: %v\n", req.verb, err)
		return startupExitError
	}
	if prep.Refused {
		if req.asJSON {
			_ = writeStartupJSON(stdout, stderr, prep)
		} else {
			printRefusal(stdout, prep.Message, prep.Suggested)
		}
		return startupExitRefused
	}
	// With --json the plan and prompt go to stderr so stdout stays one object.
	planOut := stdout
	if req.asJSON {
		planOut = stderr
	}
	printSystemPlan(planOut, prep)
	if ok, code := confirmSystemChange(req, planOut, stderr, sys); !ok {
		return code
	}
	res, err := c.LaunchdSystemChange(req.verb, prep.Token)
	if err != nil {
		if helperclient.IsRateLimited(err) {
			fmt.Fprintf(stderr, "startup %s: too many changes; try again later (%v)\n", req.verb, err)
		} else {
			fmt.Fprintf(stderr, "startup %s: %v\n", req.verb, err)
		}
		return startupExitError
	}
	if req.asJSON {
		if code := writeStartupJSON(stdout, stderr, res); code != startupExitOK {
			return code
		}
	} else {
		printSystemChange(stdout, res)
	}
	return systemChangeExit(req, res, stderr)
}

func systemChangeExit(req startupSystemRequest, res helper.LaunchdChangeResult, stderr io.Writer) int {
	switch {
	case res.Refused:
		return startupExitRefused
	case res.Outcome == helper.OutcomeError:
		fmt.Fprintf(stderr, "startup %s: %s\n", req.verb, res.Message)
		return startupExitError
	case res.Mismatch != "" || res.Outcome == helper.OutcomeMismatch:
		fmt.Fprintf(stderr, "startup %s: %s did not take effect: %s\n", req.verb, req.label, res.Mismatch)
		return startupExitError
	}
	return startupExitOK
}

// confirmSystemChange asks y/N on a terminal; without one, --yes is
// required. A declined or unconfirmed change leaves the token unused and
// exits as refused.
func confirmSystemChange(req startupSystemRequest, w, stderr io.Writer, sys startupSystem) (bool, int) {
	if req.yes {
		return true, 0
	}
	if !sys.isTTY() {
		fmt.Fprintf(stderr, "startup %s: not changed: confirmation needs a terminal; pass --yes to confirm without one\n", req.verb)
		return false, startupExitRefused
	}
	verb := strings.ToUpper(req.verb[:1]) + req.verb[1:]
	fmt.Fprintf(w, "%s %s for all users? [y/N] ", verb, req.label)
	line, _ := bufio.NewReader(sys.stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, 0
	}
	fmt.Fprintln(w, "Not changed.")
	return false, startupExitRefused
}

// startupSystemFallback is today's system-scope refusal plus a hint, used
// when the helper is not installed, refuses the connection, or is too old
// to have the launchd methods.
func startupSystemFallback(req startupSystemRequest, stdout, stderr io.Writer, opts startup.Options, hint string) int {
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()
	res, err := startup.SystemScopeRefusal(ctx, opts, req.verb, req.label)
	if err != nil {
		fmt.Fprintf(stderr, "startup %s: %v\n", req.verb, err)
		return startupExitError
	}
	if req.asJSON {
		_ = writeStartupJSON(stdout, stderr, res)
		fmt.Fprintln(stderr, hint)
		return startupExitRefused
	}
	printStartupChange(stdout, res)
	fmt.Fprintln(stdout, hint)
	return startupExitRefused
}

func printRefusal(w io.Writer, msg string, suggested []string) {
	fmt.Fprintf(w, "Not changed: %s\n", msg)
	for _, c := range suggested {
		fmt.Fprintf(w, "  %s\n", c)
	}
}

func printSystemPlan(w io.Writer, p helper.LaunchdPrepareResult) {
	fmt.Fprintf(w, "System launch daemon %s\n", p.Label)
	if t := p.Target; t != nil {
		fmt.Fprintf(w, "  plist:   %s (sha256 %s)\n", t.PlistPath, t.PlistSHA256)
		fmt.Fprintf(w, "  program: %s\n", t.Program)
		fmt.Fprintf(w, "  bundle:  %s (%s)\n", t.BundlePath, t.BundleID)
		fmt.Fprintf(w, "  team id: %s\n", t.TeamID)
	}
	if b := p.Before; b != nil {
		fmt.Fprintf(w, "  now:     %s\n", launchdStateText(*b))
	}
	fmt.Fprintln(w, "Will run:")
	for _, argv := range p.Planned {
		fmt.Fprintf(w, "  %s\n", strings.Join(argv, " "))
	}
}

func printSystemChange(w io.Writer, res helper.LaunchdChangeResult) {
	if res.Refused {
		printRefusal(w, res.Message, res.Suggested)
		return
	}
	for _, c := range res.Commands {
		fmt.Fprintf(w, "ran: %s (exit %d) via %s\n", strings.Join(c.Argv, " "), c.ExitCode, res.Via)
		if c.Stderr != "" {
			fmt.Fprintf(w, "     %s\n", c.Stderr)
		}
	}
	for _, argv := range res.Skipped {
		fmt.Fprintf(w, "skipped: %s\n", strings.Join(argv, " "))
	}
	if res.Message != "" {
		fmt.Fprintf(w, "note: %s\n", res.Message)
	}
	for _, c := range res.Suggested {
		fmt.Fprintf(w, "  %s\n", c)
	}
	if res.After != nil {
		fmt.Fprintf(w, "%s is now %s\n", res.Label, launchdStateText(*res.After))
	}
}

// launchdStateText says loaded and running separately: enable loads a job,
// and an on-demand job does not run until something triggers it.
func launchdStateText(s helper.LaunchdState) string {
	state := "enabled"
	if s.Disabled {
		state = "disabled"
	}
	switch {
	case s.Running && s.PID > 0:
		state += fmt.Sprintf(",loaded,running (pid %d)", s.PID)
	case s.Running:
		state += ",loaded,running"
	case s.Loaded:
		state += ",loaded,not running"
	default:
		state += ",not loaded"
	}
	return state
}
