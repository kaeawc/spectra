package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kaeawc/spectra/internal/helperclient"
)

// helperBinaryDest is where spectra-helper is installed.
const helperBinaryDest = "/Library/PrivilegedHelperTools/spectra-helper"

// helperPlist is the LaunchDaemon plist path.
const helperPlist = "/Library/LaunchDaemons/dev.spectra.helper.plist"

const helperGroup = "_spectra"

const helperLogPath = "/var/log/spectra-helper.log"

const helperNewsyslogConf = "/etc/newsyslog.d/spectra-helper.conf"

// helperPlistContent is the LaunchDaemon plist that starts spectra-helper
// at boot and keeps it alive.
const helperPlistContent = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.spectra.helper</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Library/PrivilegedHelperTools/spectra-helper</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardErrorPath</key>
    <string>` + helperLogPath + `</string>
</dict>
</plist>
`

const helperNewsyslogContent = helperLogPath + `	root:wheel	640	7	1024	*	J
`

type helperStatusClient interface {
	Available() bool
	Health() (map[string]any, error)
}

type helperInstallDeps struct {
	executable func() (string, error)
	stat       func(string) (os.FileInfo, error)
	getenv     func(string) string
	runCheck   commandRunner
	runRoot    rootRunner
	writeTemp  tempTextWriter
	client     func() helperStatusClient
}

func runInstallHelper(args []string) int {
	fs := flag.NewFlagSet("spectra install-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	requireSigned := fs.Bool("require-signed", false, "require spectra-helper to carry a valid Developer ID signature before installing")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	opts := helperInstallOptions{RequireSigned: *requireSigned}
	if err := installHelper(defaultHelperInstallDeps(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type helperInstallOptions struct {
	RequireSigned bool
}

func installHelper(deps helperInstallDeps, opts helperInstallOptions) error {
	// Find the spectra-helper binary next to the running spectra binary.
	self, err := deps.executable()
	if err != nil {
		return fmt.Errorf("install-helper: could not find executable path: %w", err)
	}
	helperSrc := filepath.Join(filepath.Dir(self), "spectra-helper")
	if _, err := deps.stat(helperSrc); err != nil {
		return fmt.Errorf("install-helper: spectra-helper binary not found at %s\nBuild it with: go build ./cmd/spectra-helper", helperSrc)
	}
	if opts.RequireSigned || truthyEnv(deps.getenv("SPECTRA_REQUIRE_SIGNED_HELPER")) {
		if err := verifyHelperSignature(helperSrc, deps.runCheck); err != nil {
			return err
		}
	}

	fmt.Fprintf(os.Stderr, "This requires administrator privilege. You may be prompted for your password.\n\n")

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Create _spectra group", func() error {
			return ensureHelperGroup(deps.runRoot)
		}},
		{"Add current user to _spectra group", func() error {
			user := helperInstallUser(deps.getenv)
			if user == "" || user == "root" {
				return nil
			}
			return deps.runRoot("dseditgroup", "-o", "edit", "-a", user, "-t", "user", helperGroup)
		}},
		{"Create /Library/PrivilegedHelperTools/", func() error {
			return deps.runRoot("mkdir", "-p", "/Library/PrivilegedHelperTools")
		}},
		{"Copy spectra-helper binary", func() error {
			return deps.runRoot("cp", helperSrc, helperBinaryDest)
		}},
		{"Set ownership and permissions", func() error {
			if err := deps.runRoot("chown", "root:wheel", helperBinaryDest); err != nil {
				return err
			}
			return deps.runRoot("chmod", "755", helperBinaryDest)
		}},
		{"Install LaunchDaemon plist", func() error {
			return installRootTextFile(helperPlist, helperPlistContent, deps.runRoot, deps.writeTemp)
		}},
		{"Install helper log rotation", func() error {
			return installRootTextFile(helperNewsyslogConf, helperNewsyslogContent, deps.runRoot, deps.writeTemp)
		}},
		{"Load LaunchDaemon", func() error {
			return deps.runRoot("launchctl", "load", "-w", helperPlist)
		}},
	}

	for _, step := range steps {
		fmt.Printf("  • %s… ", step.name)
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			return err
		}
		fmt.Println("done")
	}

	fmt.Println()
	fmt.Println("spectra-helper installed successfully.")
	fmt.Println()
	fmt.Println("To grant Full Disk Access (required for TCC.db queries):")
	fmt.Println("  System Settings → Privacy & Security → Full Disk Access → add spectra-helper")
	fmt.Println()
	fmt.Println("If this is the first install, log out and back in so group membership is refreshed.")
	fmt.Println()
	fmt.Println("Verify with: spectra install-helper --status")
	return nil
}

func verifyHelperSignature(path string, run commandRunner) error {
	if run == nil {
		run = runCommand
	}
	if out, err := run("codesign", "-dv", "--verbose=4", path); err != nil {
		return fmt.Errorf("install-helper: %s is not signed with a verifiable Developer ID signature: %w\n%s", path, err, strings.TrimSpace(out))
	} else if !strings.Contains(out, "Authority=Developer ID Application:") {
		return fmt.Errorf("install-helper: %s is signed, but not with a Developer ID Application certificate", path)
	}
	if out, err := run("codesign", "--verify", "--strict", "--verbose=2", path); err != nil {
		return fmt.Errorf("install-helper: %s failed strict code-signature verification: %w\n%s", path, err, strings.TrimSpace(out))
	}
	return nil
}

func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func ensureHelperGroup(run rootRunner) error {
	if err := run("dseditgroup", "-o", "read", helperGroup); err == nil {
		return nil
	}
	return run("dseditgroup", "-o", "create", helperGroup)
}

func helperInstallUser(getenv func(string) string) string {
	if user := getenv("SUDO_USER"); user != "" {
		return user
	}
	return getenv("USER")
}

type rootRunner func(name string, args ...string) error

type tempTextWriter func(pattern, content string) (path string, cleanup func(), err error)

func installRootTextFile(dest, content string, run rootRunner, write tempTextWriter) error {
	tmpPath, cleanup, err := write("spectra-helper-*", content)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}
	return run("cp", tmpPath, dest)
}

func writeTempText(pattern, content string) (string, func(), error) {
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

func runUninstallHelper(args []string) int {
	fs := flag.NewFlagSet("spectra uninstall-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := uninstallHelper(defaultHelperInstallDeps()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func uninstallHelper(deps helperInstallDeps) error {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"Unload LaunchDaemon", func() error {
			return deps.runRoot("launchctl", "unload", "-w", helperPlist)
		}},
		{"Remove plist", func() error {
			return deps.runRoot("rm", "-f", helperPlist)
		}},
		{"Remove helper log rotation", func() error {
			return deps.runRoot("rm", "-f", helperNewsyslogConf)
		}},
		{"Remove binary", func() error {
			return deps.runRoot("rm", "-f", helperBinaryDest)
		}},
	}

	for _, step := range steps {
		fmt.Printf("  • %s… ", step.name)
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			return err
		}
		fmt.Println("done")
	}

	fmt.Println("\nspectra-helper uninstalled.")
	return nil
}

func runHelperStatus(args []string) int {
	fs := flag.NewFlagSet("spectra install-helper --status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := helperStatus(defaultHelperInstallDeps()); err != nil {
		if helperclient.IsUnavailable(err) {
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func helperStatus(deps helperInstallDeps) error {
	c := deps.client()
	if !c.Available() {
		fmt.Println("spectra-helper: not running (socket unreachable)")
		return helperclient.ErrHelperUnavailable
	}
	h, err := c.Health()
	if err != nil {
		return fmt.Errorf("spectra-helper health check failed: %w", err)
	}
	fmt.Printf("spectra-helper: running — %v\n", h)
	return nil
}

func defaultHelperInstallDeps() helperInstallDeps {
	return helperInstallDeps{
		executable: os.Executable,
		stat:       os.Stat,
		getenv:     os.Getenv,
		runCheck:   runCommand,
		runRoot:    sudoRun,
		writeTemp:  writeTempText,
		client: func() helperStatusClient {
			return helperclient.New()
		},
	}
}

type commandRunner func(name string, args ...string) (string, error)

func runCommand(name string, args ...string) (string, error) {
	// #nosec G204 -- command names are fixed at call sites.
	cmd := exec.Command(name, args...)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func sudoRun(name string, args ...string) error {
	if !sudoCommandAllowed(name) {
		return fmt.Errorf("sudo command %q is not allowlisted", name)
	}
	// #nosec G204 -- sudo is invoked only for the fixed helper management commands.
	cmd := exec.Command("sudo", append([]string{name}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func sudoCommandAllowed(name string) bool {
	switch name {
	case "chmod", "chown", "cp", "dseditgroup", "launchctl", "mkdir", "rm":
		return true
	default:
		return false
	}
}

// installHelperCmd dispatches install-helper subcommands.
func runInstallHelperCmd(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "--status", "status":
			return runHelperStatus(args[1:])
		case "uninstall":
			return runUninstallHelper(args[1:])
		}
	}
	return runInstallHelper(args)
}
