// Command spectra-helper is the Spectra privileged daemon. It must run as
// root (installed as a LaunchDaemon) and exposes a narrow JSON-RPC 2.0
// surface over a local Unix socket for root-only telemetry:
//
//   - System TCC.db queries
//   - powermetrics samples
//   - fs_usage traces for a specific PID
//   - Full process tree (including system daemons)
//
// The socket is /var/run/spectra-helper.sock (0660 root:_spectra).
// Only processes in the _spectra group can connect.
//
// See docs/design/privileged-helper.md for the full design.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/helper"
	"github.com/kaeawc/spectra/internal/hostos"
)

var version = "dev"
var lookupGroup = user.LookupGroup

// sockPath is a var only so tests can point it at a temp socket.
var sockPath = "/var/run/spectra-helper.sock"

// liveProbeTimeout bounds the dial used to detect an already-running helper.
const liveProbeTimeout = 500 * time.Millisecond

const usageText = `usage: spectra-helper [--version | --help]

Runs the Spectra privileged helper. It must run as root and is normally
started by launchd or systemd with no arguments.

  --version, version   print the helper version and exit
  -h, --help, help     print this help and exit
`

// helperGroup is the Unix group that owns the helper socket. macOS uses the
// underscore-prefixed service-account convention; Linux does not.
var helperGroup = defaultHelperGroup()

func defaultHelperGroup() string {
	if hostos.Current() == hostos.Linux {
		return "spectra"
	}
	return "_spectra"
}

func main() {
	os.Exit(runWithArgs(os.Args[1:]))
}

func runWithArgs(args []string) int {
	return dispatchArgs(args, os.Stdout, os.Stderr, run)
}

// dispatchArgs validates arguments before serve is reached, so --help, a
// typo, or any unexpected argument never touches the live socket.
func dispatchArgs(args []string, stdout, stderr io.Writer, serve func() int) int {
	switch {
	case len(args) == 0:
		return serve()
	case helperVersionArg(args):
		fmt.Fprintln(stdout, version)
		return 0
	case helperHelpArg(args):
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "spectra-helper: unexpected arguments %q\n", args)
		fmt.Fprint(stderr, usageText)
		return 2
	}
}

func helperVersionArg(args []string) bool {
	return len(args) == 1 && (args[0] == "--version" || args[0] == "version")
}

func helperHelpArg(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help")
}

func run() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// #nosec G301 -- /var/run must remain traversable so group ACLs on the socket matter.
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: mkdir: %v\n", err)
		return 1
	}
	if err := clearStaleSocket(sockPath); err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: %v\n", err)
		return 1
	}

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper: listen: %v\n", err)
		return 1
	}
	if err := secureSocket(sockPath, helperGroup, os.Chown, os.Chmod); err != nil {
		ln.Close()
		fmt.Fprintf(os.Stderr, "spectra-helper: secure socket: %v\n", err)
		return 1
	}
	defer func() {
		ln.Close()
		os.Remove(sockPath)
	}()

	d := helper.NewDispatcher()
	d.SetAuditWriter(os.Stderr)
	d.SetRateLimit(120, time.Minute)
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

// clearStaleSocket removes path only when it is a Unix socket that nothing
// accepts connections on (left behind by a crashed helper). It refuses to
// start over a live helper and never removes a non-socket file.
func clearStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace %s: exists and is not a socket (mode %s)", path, info.Mode())
	}
	conn, err := net.DialTimeout("unix", path, liveProbeTimeout)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("another spectra-helper is already listening on %s", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("probe existing socket %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket %s: %w", path, err)
	}
	return nil
}

func secureSocket(path, group string, chown func(string, int, int) error, chmod func(string, os.FileMode) error) error {
	gid, err := groupID(group)
	if err != nil {
		return err
	}
	if err := chown(path, 0, gid); err != nil {
		return fmt.Errorf("chown root:%s: %w", group, err)
	}
	// 0660: accessible to root and _spectra group members only.
	// #nosec G302 -- helper socket intentionally grants _spectra group access.
	if err := chmod(path, 0o660); err != nil {
		return fmt.Errorf("chmod 0660: %w", err)
	}
	return nil
}

func groupID(name string) (int, error) {
	g, err := lookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("lookup group %s: %w", name, err)
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("parse group %s gid %q: %w", name, g.Gid, err)
	}
	return gid, nil
}
