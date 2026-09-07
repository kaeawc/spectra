package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kaeawc/spectra/internal/helperclient"
)

// runInstallHelperLinux parses flags and installs the helper via systemd.
func runInstallHelperLinux(args []string) int {
	fs := flag.NewFlagSet("spectra install-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	requireSigned := fs.Bool("require-signed", false, "macOS-only; ignored on Linux")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := installHelperLinux(defaultHelperInstallDepsLinux(), helperInstallOptions{RequireSigned: *requireSigned}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runUninstallHelperLinux uninstalls the systemd helper.
func runUninstallHelperLinux(args []string) int {
	fs := flag.NewFlagSet("spectra uninstall-helper", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := uninstallHelperLinux(defaultHelperInstallDepsLinux()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// Linux privileged-helper install locations and identity.
const (
	helperBinaryDestLinux = "/usr/local/lib/spectra/spectra-helper"
	helperSystemdUnitPath = "/etc/systemd/system/spectra-helper.service"
	helperGroupLinux      = "spectra"
)

// helperSystemdUnitContent runs the helper as root at boot and keeps it
// alive. It logs to journald (StandardError/Output default), so no separate
// logfile or rotation config is needed as on macOS.
const helperSystemdUnitContent = `[Unit]
Description=Spectra privileged helper
After=network.target

[Service]
Type=simple
ExecStart=` + helperBinaryDestLinux + `
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`

func installHelperLinux(deps helperInstallDeps, opts helperInstallOptions) error {
	self, err := deps.executable()
	if err != nil {
		return fmt.Errorf("install-helper: could not find executable path: %w", err)
	}
	helperSrc := filepath.Join(filepath.Dir(self), "spectra-helper")
	if _, err := deps.stat(helperSrc); err != nil {
		return fmt.Errorf("install-helper: spectra-helper binary not found at %s\nBuild it with: go build ./cmd/spectra-helper", helperSrc)
	}
	if opts.RequireSigned || truthyEnv(deps.getenv("SPECTRA_REQUIRE_SIGNED_HELPER")) {
		fmt.Fprintln(os.Stderr, "install-helper: --require-signed is a macOS (codesign) option and is ignored on Linux.")
	}

	fmt.Fprintf(os.Stderr, "This requires administrator privilege. You may be prompted for your password.\n\n")

	steps := []struct {
		name string
		fn   func() error
	}{
		{"Create spectra group", func() error {
			return deps.runRoot("groupadd", "-f", helperGroupLinux)
		}},
		{"Add current user to spectra group", func() error {
			user := helperInstallUser(deps.getenv)
			if user == "" || user == "root" {
				return nil
			}
			return deps.runRoot("usermod", "-aG", helperGroupLinux, user)
		}},
		{"Create /usr/local/lib/spectra/", func() error {
			return deps.runRoot("mkdir", "-p", filepath.Dir(helperBinaryDestLinux))
		}},
		{"Copy spectra-helper binary", func() error {
			return deps.runRoot("cp", helperSrc, helperBinaryDestLinux)
		}},
		{"Set ownership and permissions", func() error {
			if err := deps.runRoot("chown", "root:root", helperBinaryDestLinux); err != nil {
				return err
			}
			return deps.runRoot("chmod", "755", helperBinaryDestLinux)
		}},
		{"Install systemd unit", func() error {
			return installRootTextFile(helperSystemdUnitPath, helperSystemdUnitContent, deps.runRoot, deps.writeTemp)
		}},
		{"Reload systemd", func() error {
			return deps.runRoot("systemctl", "daemon-reload")
		}},
		{"Enable and start helper", func() error {
			return deps.runRoot("systemctl", "enable", "--now", "spectra-helper.service")
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
	fmt.Println("If this is the first install, log out and back in so group membership is refreshed.")
	fmt.Println()
	fmt.Println("Verify with: spectra install-helper --status")
	return nil
}

func uninstallHelperLinux(deps helperInstallDeps) error {
	steps := []struct {
		name string
		fn   func() error
	}{
		{"Disable and stop helper", func() error {
			return deps.runRoot("systemctl", "disable", "--now", "spectra-helper.service")
		}},
		{"Remove systemd unit", func() error {
			return deps.runRoot("rm", "-f", helperSystemdUnitPath)
		}},
		{"Reload systemd", func() error {
			return deps.runRoot("systemctl", "daemon-reload")
		}},
		{"Remove binary", func() error {
			return deps.runRoot("rm", "-f", helperBinaryDestLinux)
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

func defaultHelperInstallDepsLinux() helperInstallDeps {
	return helperInstallDeps{
		executable: os.Executable,
		stat:       os.Stat,
		getenv:     os.Getenv,
		runCheck:   runCommand,
		runRoot:    sudoRunLinux,
		writeTemp:  writeTempText,
		client: func() helperStatusClient {
			return helperclient.New()
		},
	}
}

func sudoRunLinux(name string, args ...string) error {
	if !sudoCommandAllowedLinux(name) {
		return fmt.Errorf("sudo command %q is not allowlisted", name)
	}
	// #nosec G204 -- sudo is invoked only for the fixed helper management commands.
	cmd := exec.Command("sudo", append([]string{name}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func sudoCommandAllowedLinux(name string) bool {
	switch name {
	case "chmod", "chown", "cp", "groupadd", "mkdir", "rm", "systemctl", "usermod":
		return true
	default:
		return false
	}
}
