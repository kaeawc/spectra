// Command spectra-helper is the Spectra privileged daemon. It must run as
// root (installed as a LaunchDaemon) and exposes a narrow JSON-RPC 2.0
// surface over a local Unix socket for root-only telemetry:
//
//   - System TCC.db queries
//   - powermetrics samples
//   - Full process tree (including system daemons)
//
// The socket is /var/run/spectra-helper.sock (0660 root:_spectra).
// Only processes in the _spectra group can connect.
//
// See docs/design/privileged-helper.md for the full design.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/kaeawc/spectra/internal/helper"
)

var version = "dev"

const sockPath = "/var/run/spectra-helper.sock"

func main() {
	os.Exit(runWithArgs(os.Args[1:]))
}

func runWithArgs(args []string) int {
	if helperVersionArg(args) {
		fmt.Println(version)
		return 0
	}
	return run()
}

func helperVersionArg(args []string) bool {
	return len(args) == 1 && (args[0] == "--version" || args[0] == "version")
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// #nosec G301 -- /var/run must remain traversable so group ACLs on the socket matter.
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: mkdir: %v\n", err)
		return 1
	}
	// Remove stale socket from a previous crash.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: listen: %v\n", err)
		return 1
	}
	// 0660: accessible to root and _spectra group members only.
	// #nosec G302 -- helper socket intentionally grants _spectra group access.
	if err := os.Chmod(sockPath, 0o660); err != nil {
		ln.Close()
		fmt.Fprintf(os.Stderr, "spectra-helper: chmod: %v\n", err)
		return 1
	}
	defer func() {
		ln.Close()
		os.Remove(sockPath)
	}()

	d := helper.NewDispatcher()
	d.SetAuditWriter(os.Stderr)
	helper.RegisterAll(d, nil) // nil → real commands

	fmt.Fprintf(os.Stderr, "spectra-helper %s: listening on %s\n", version, sockPath)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	if err := d.ServeListener(ln); err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: %v\n", err)
		return 1
	}
	return 0
}
