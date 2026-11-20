package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/owner"
)

const (
	ownerExitFound   = 0
	ownerExitError   = 1
	ownerExitUsage   = 2
	ownerExitNoOwner = 3
	// ownerExitUnderIncomplete: --under saw nobody, but some processes could
	// not be read, so the tree may still be in use.
	ownerExitUnderIncomplete = 4
	ownerTimeout             = 30 * time.Second
)

const ownerUsage = `usage: spectra owner [--json] [--timeout 30s] <target>
       spectra owner --under [--json] [--timeout 30s] DIR [DIR...]

Who has this? Target forms:
  :8080  tcp:8080  udp:53   processes listening on or connected from a local port
  /path/to/file             processes holding it open (fd and mode), as cwd, or as executable
  1234                      that pid's user, executable and start time

Each answer lists pid, user, command, how the target is held, and the parent chain.
Without root only your own processes are visible and the answer says so.

--under lists every visible process whose working directory is DIR or below
it (both compared as given and symlink-resolved), with pid, command, cwd, start
time and parent chain. Spectra itself and its own children are left out.

--timeout bounds the whole run; reaching it is an error (exit 1), never
"none found". Flags may come before or after the target or directories; use
-- to end flags before a name that starts with "-".

Exit codes: 0 owner found, 1 error, 2 usage, 3 no owner found.
With --under: 3 only when every process was read; 4 when none is visible but
some processes could not be read (re-run with sudo before removing the tree).
`

// ownerFind resolves a target; tests substitute it.
type ownerFind func(ctx context.Context, t owner.Target) (owner.Result, error)

// ownerUnder lists processes working under directories; tests substitute it.
type ownerUnder func(ctx context.Context, dirs []string) (owner.UnderResult, error)

func runOwner(args []string) int {
	cwd, _ := os.Getwd()
	return runOwnerWithIO(args, os.Stdout, os.Stderr, cwd, owner.Finder{}.Find, owner.Finder{}.Under)
}

func runOwnerWithIO(args []string, stdout, stderr io.Writer, cwd string, find ownerFind, under ownerUnder) int {
	fs := flag.NewFlagSet("spectra owner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, ownerUsage) }
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	underMode := fs.Bool("under", false, "Treat the arguments as directories and list processes whose cwd is inside them")
	timeout := fs.Duration("timeout", ownerTimeout, "Give up (exit 1) after this long")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return ownerExitUsage
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "owner: --timeout must be positive")
		return ownerExitUsage
	}
	if *underMode {
		return runOwnerUnder(pos, stdout, stderr, cwd, *asJSON, *timeout, under)
	}
	if len(pos) != 1 {
		fs.Usage()
		return ownerExitUsage
	}
	t, err := owner.ParseTarget(pos[0], cwd)
	if err != nil {
		fmt.Fprintf(stderr, "owner: %v\n", err)
		return ownerExitUsage
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res, err := find(ctx, t)
	if err != nil {
		fmt.Fprintf(stderr, "owner: %v\n", err)
		return ownerExitError
	}
	if res.Holders == nil {
		res.Holders = []owner.Holder{}
	}
	if *asJSON {
		if err := writeOwnerJSON(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return ownerExitError
		}
	} else {
		printOwner(stdout, res)
	}
	return ownerExitFor(len(res.Holders))
}

func ownerExitFor(holders int) int {
	if holders == 0 {
		return ownerExitNoOwner
	}
	return ownerExitFound
}

// underExitFor never reports "none" for a scan that could not read every
// process: a cleanup script must not take that as "the tree is free".
func underExitFor(r owner.UnderResult) int {
	switch {
	case len(r.Holders) > 0:
		return ownerExitFound
	case r.Incomplete:
		return ownerExitUnderIncomplete
	}
	return ownerExitNoOwner
}

func writeOwnerJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func runOwnerUnder(args []string, stdout, stderr io.Writer, cwd string, asJSON bool, timeout time.Duration, under ownerUnder) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, ownerUsage)
		return ownerExitUsage
	}
	dirs := make([]string, len(args))
	for i, a := range args {
		if !filepath.IsAbs(a) {
			a = filepath.Join(cwd, a)
		}
		dirs[i] = filepath.Clean(a)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := under(ctx, dirs)
	if err != nil {
		fmt.Fprintf(stderr, "owner: %v\n", err)
		return ownerExitError
	}
	if res.Holders == nil {
		res.Holders = []owner.Holder{}
	}
	if res.Dirs == nil {
		res.Dirs = dirs
	}
	if asJSON {
		if err := writeOwnerJSON(stdout, res); err != nil {
			fmt.Fprintln(stderr, err)
			return ownerExitError
		}
	} else {
		printOwnerUnder(stdout, res)
	}
	return underExitFor(res)
}

func printOwnerUnder(w io.Writer, r owner.UnderResult) {
	where := strings.Join(r.Dirs, ", ")
	switch {
	case len(r.Holders) == 0 && r.Incomplete:
		fmt.Fprintf(w, "No visible process is working under %s, but some processes could not be read: %s.\n", where, r.Hint)
		return
	case len(r.Holders) == 0:
		fmt.Fprintf(w, "No process is working under %s.\n", where)
	default:
		fmt.Fprintf(w, "Processes working under %s:\n", where)
	}
	for _, h := range r.Holders {
		fmt.Fprintf(w, "  %d  %s  %s\n", h.PID, orDash(h.User), orDash(h.Command))
		fmt.Fprintf(w, "      cwd: %s\n", h.Cwd)
		printHolderDetails(w, h)
	}
	if r.Incomplete {
		fmt.Fprintf(w, "Note: result may be incomplete: %s.\n", r.Hint)
	}
}

func printHolderDetails(w io.Writer, h owner.Holder) {
	if h.Executable != "" {
		fmt.Fprintf(w, "      exe: %s\n", h.Executable)
	}
	if !h.Started.IsZero() {
		fmt.Fprintf(w, "      started: %s\n", h.Started.Format(time.RFC3339))
	}
	if len(h.Chain) > 0 {
		parts := make([]string, len(h.Chain))
		for i, l := range h.Chain {
			parts[i] = fmt.Sprintf("%s (%d)", l.Command, l.PID)
		}
		fmt.Fprintf(w, "      parents: %s\n", strings.Join(parts, " <- "))
	}
}

func printOwner(w io.Writer, r owner.Result) {
	if len(r.Holders) == 0 {
		if r.Incomplete {
			fmt.Fprintf(w, "No visible owner of %s, but the result may be incomplete: %s.\n", r.Target.Raw, r.Hint)
		} else {
			fmt.Fprintf(w, "No owner found for %s.\n", r.Target.Raw)
		}
		return
	}
	fmt.Fprintf(w, "Owners of %s:\n", r.Target.Raw)
	for _, h := range r.Holders {
		hold := h.Hold
		if h.Mode != "" {
			hold += " (" + h.Mode + ")"
		}
		if h.Detail != "" {
			hold += " " + h.Detail
		}
		fmt.Fprintf(w, "  %d  %s  %s  %s\n", h.PID, orDash(h.User), orDash(h.Command), hold)
		printHolderDetails(w, h)
	}
	if r.Incomplete {
		fmt.Fprintf(w, "Note: result may be incomplete: %s.\n", r.Hint)
	}
}
