package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/helperclient"
)

// helperBinaryDest is where spectra-helper is installed.
const helperBinaryDest = "/Library/PrivilegedHelperTools/spectra-helper"

// helperPlist is the LaunchDaemon plist path.
const helperPlist = "/Library/LaunchDaemons/dev.spectra.helper.plist"

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
    <string>/var/log/spectra-helper.log</string>
</dict>
</plist>
`

func runInstallHelper(args []string) int {
	fs := flag.NewFlagSet("spectra install-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Find the spectra-helper binary next to the running spectra binary.
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "install-helper: could not find executable path:", err)
		return 1
	}
	helperSrc := filepath.Join(filepath.Dir(self), "spectra-helper")
	if _, err := os.Stat(helperSrc); err != nil {
		fmt.Fprintf(os.Stderr, "install-helper: spectra-helper binary not found at %s\n", helperSrc)
		fmt.Fprintln(os.Stderr, "Build it with: go build ./cmd/spectra-helper")
		return 1
	}

	fmt.Fprintf(os.Stderr, "This requires administrator privilege. You may be prompted for your password.\n\n")

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Create /Library/PrivilegedHelperTools/", func() error {
			return sudoRun("mkdir", "-p", "/Library/PrivilegedHelperTools")
		}},
		{"Copy spectra-helper binary", func() error {
			return sudoRun("cp", helperSrc, helperBinaryDest)
		}},
		{"Set ownership and permissions", func() error {
			if err := sudoRun("chown", "root:wheel", helperBinaryDest); err != nil {
				return err
			}
			return sudoRun("chmod", "755", helperBinaryDest)
		}},
		{"Install LaunchDaemon plist", func() error {
			// Write plist to a temp file, then sudo-copy it.
			tmp, err := os.CreateTemp("", "spectra-helper-*.plist")
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if _, err := tmp.WriteString(helperPlistContent); err != nil {
				return err
			}
			tmp.Close()
			return sudoRun("cp", tmp.Name(), helperPlist)
		}},
		{"Load LaunchDaemon", func() error {
			return sudoRun("launchctl", "load", "-w", helperPlist)
		}},
	}

	for _, step := range steps {
		fmt.Printf("  • %s… ", step.name)
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("done")
	}

	fmt.Println()
	fmt.Println("spectra-helper installed successfully.")
	fmt.Println()
	fmt.Println("To grant Full Disk Access (required for TCC.db queries):")
	fmt.Println("  System Settings → Privacy & Security → Full Disk Access → add spectra-helper")
	fmt.Println()
	fmt.Println("Verify with: spectra install-helper --status")
	return 0
}

func runUninstallHelper(args []string) int {
	fs := flag.NewFlagSet("spectra uninstall-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Unload LaunchDaemon", func() error {
			return sudoRun("launchctl", "unload", "-w", helperPlist)
		}},
		{"Remove plist", func() error {
			return sudoRun("rm", "-f", helperPlist)
		}},
		{"Remove binary", func() error {
			return sudoRun("rm", "-f", helperBinaryDest)
		}},
	}

	for _, step := range steps {
		fmt.Printf("  • %s… ", step.name)
		if err := step.fn(); err != nil {
			fmt.Println("FAILED")
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("done")
	}

	fmt.Println("\nspectra-helper uninstalled.")
	return 0
}

func runHelperStatus(args []string) int {
	fs := flag.NewFlagSet("spectra install-helper --status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	c := helperclient.New()
	if !c.Available() {
		fmt.Println("spectra-helper: not running (socket unreachable)")
		return 1
	}
	h, err := c.Health()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spectra-helper health check failed: %v\n", err)
		return 1
	}
	fmt.Printf("spectra-helper: running — %v\n", h)
	return 0
}

func sudoRun(name string, args ...string) error {
	// #nosec G204 -- sudo is invoked only for the fixed helper management commands.
	cmd := exec.Command("sudo", append([]string{name}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
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
