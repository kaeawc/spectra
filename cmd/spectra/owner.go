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

	"github.com/kaeawc/spectra/internal/owner"
)

const (
	ownerExitFound   = 0
	ownerExitError   = 1
	ownerExitUsage   = 2
	ownerExitNoOwner = 3
	ownerTimeout     = 30 * time.Second
)

const ownerUsage = `usage: spectra owner [--json] <target>

Who has this? Target forms:
  :8080  tcp:8080  udp:53   processes listening on or connected from a local port
  /path/to/file             processes holding it open (fd and mode), as cwd, or as executable
  1234                      that pid's user, executable and start time

Each answer lists pid, user, command, how the target is held, and the parent chain.
Without root only your own processes are visible and the answer says so.

Exit codes: 0 owner found, 1 error, 2 usage, 3 no owner found.
`

// ownerFind resolves a target; tests substitute it.
type ownerFind func(ctx context.Context, t owner.Target) (owner.Result, error)

func runOwner(args []string) int {
	cwd, _ := os.Getwd()
	return runOwnerWithIO(args, os.Stdout, os.Stderr, cwd, owner.Finder{}.Find)
}

func runOwnerWithIO(args []string, stdout, stderr io.Writer, cwd string, find ownerFind) int {
	fs := flag.NewFlagSet("spectra owner", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, ownerUsage) }
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return ownerExitUsage
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
	ctx, cancel := context.WithTimeout(context.Background(), ownerTimeout)
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
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			fmt.Fprintln(stderr, err)
			return ownerExitError
		}
	} else {
		printOwner(stdout, res)
	}
	if len(res.Holders) == 0 {
		return ownerExitNoOwner
	}
	return ownerExitFound
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
	if r.Incomplete {
		fmt.Fprintf(w, "Note: result may be incomplete: %s.\n", r.Hint)
	}
}
