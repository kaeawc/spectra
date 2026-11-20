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
	ownerTimeout     = 30 * time.Second
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
"none found".

Exit codes: 0 owner found, 1 error, 2 usage, 3 no owner found.
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
	if err := fs.Parse(args); err != nil {
		return ownerExitUsage
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "owner: --timeout must be positive")
		return ownerExitUsage
	}
	if *underMode {
		return runOwnerUnder(fs.Args(), stdout, stderr, cwd, *asJSON, *timeout, under)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return ownerExitUsage
	}
	t, err := owner.ParseTarget(fs.Arg(0), cwd)
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
	return ownerExitFor(len(res.Holders))
}

func printOwnerUnder(w io.Writer, r owner.UnderResult) {
	where := strings.Join(r.Dirs, ", ")
	if len(r.Holders) == 0 {
		fmt.Fprintf(w, "No process is working under %s.\n", where)
	} else {
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
