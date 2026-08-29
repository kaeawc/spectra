package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const scheduleAgentLabel = "dev.spectra.snapshot"

// validateScheduleInterval rejects intervals that can't be represented exactly
// as the whole-second StartInterval a LaunchAgent takes, so the plist and the
// reported value never disagree.
func validateScheduleInterval(interval time.Duration) error {
	if interval < time.Minute {
		return errors.New("--interval must be at least 1m")
	}
	if interval%time.Second != 0 {
		return errors.New("--interval must be a whole number of seconds")
	}
	return nil
}

func runSchedule(args []string) int {
	return runScheduleWithIO(args, os.Stdout, os.Stderr, defaultDaemonAgentDeps())
}

func runScheduleWithIO(args []string, stdout, stderr io.Writer, deps daemonAgentDeps) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: spectra schedule <install [--interval 1h] [--no-load] | uninstall | status | print-plist>")
		return 2
	}
	switch args[0] {
	case "install":
		return runScheduleInstall(args[1:], stdout, stderr, deps)
	case "uninstall":
		return runScheduleUninstall(args[1:], stdout, stderr, deps)
	case "status":
		return runScheduleStatus(args[1:], stdout, stderr, deps)
	case "print-plist":
		return runSchedulePrintPlist(args[1:], stdout, stderr, deps)
	default:
		fmt.Fprintf(stderr, "unknown schedule subcommand %q (want: install, uninstall, status, print-plist)\n", args[0])
		return 2
	}
}

func runScheduleInstall(args []string, stdout, stderr io.Writer, deps daemonAgentDeps) int {
	fs := flag.NewFlagSet("schedule install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	interval := fs.Duration("interval", time.Hour, "how often to capture a snapshot")
	noLoad := fs.Bool("no-load", false, "write the plist but do not bootstrap with launchd")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateScheduleInterval(*interval); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	plistPath, err := installScheduleAgent(int(interval.Seconds()), *noLoad, deps)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "spectra snapshot LaunchAgent installed at %s (every %s)\n", plistPath, *interval)
	return 0
}

func runScheduleUninstall(_ []string, stdout, stderr io.Writer, deps daemonAgentDeps) int {
	plistPath, err := uninstallScheduleAgent(deps)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "spectra snapshot LaunchAgent removed from %s\n", plistPath)
	return 0
}

func runScheduleStatus(_ []string, stdout, stderr io.Writer, deps daemonAgentDeps) int {
	out, loaded, err := scheduleAgentStatus(deps)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if !loaded {
		fmt.Fprintln(stdout, "spectra snapshot LaunchAgent is not loaded")
		return 0
	}
	fmt.Fprint(stdout, string(out))
	return 0
}

func runSchedulePrintPlist(args []string, stdout, stderr io.Writer, deps daemonAgentDeps) int {
	fs := flag.NewFlagSet("schedule print-plist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	interval := fs.Duration("interval", time.Hour, "how often to capture a snapshot")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateScheduleInterval(*interval); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	exe, err := deps.executable()
	if err != nil {
		fmt.Fprintf(stderr, "resolve executable: %v\n", err)
		return 1
	}
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintf(stderr, "resolve home directory: %v\n", err)
		return 1
	}
	paths := scheduleAgentPaths(home)
	fmt.Fprint(stdout, snapshotLaunchAgentPlist(exe, int(interval.Seconds()), paths.stdoutPath, paths.stderrPath))
	return 0
}

func scheduleAgentPaths(home string) daemonAgentPathSet {
	return daemonAgentPathSet{
		launchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		logDir:          filepath.Join(home, "Library", "Logs", "Spectra"),
		plistPath:       filepath.Join(home, "Library", "LaunchAgents", scheduleAgentLabel+".plist"),
		stdoutPath:      filepath.Join(home, "Library", "Logs", "Spectra", "snapshot.launchd.out.log"),
		stderrPath:      filepath.Join(home, "Library", "Logs", "Spectra", "snapshot.launchd.err.log"),
	}
}

func installScheduleAgent(intervalSec int, noLoad bool, deps daemonAgentDeps) (string, error) {
	exe, err := deps.executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths := scheduleAgentPaths(home)
	if err := deps.mkdirAll(paths.launchAgentsDir, 0o700); err != nil {
		return "", fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := deps.mkdirAll(paths.logDir, 0o700); err != nil {
		return "", fmt.Errorf("create snapshot log directory: %w", err)
	}
	plist := snapshotLaunchAgentPlist(exe, intervalSec, paths.stdoutPath, paths.stderrPath)
	if err := deps.writeFile(paths.plistPath, []byte(plist), 0o644); err != nil {
		return "", fmt.Errorf("write LaunchAgent plist: %w", err)
	}
	if noLoad {
		return paths.plistPath, nil
	}
	domain := "gui/" + deps.uid()
	_ = deps.run("bootout", domain, paths.plistPath)
	if err := deps.run("bootstrap", domain, paths.plistPath); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %w", err)
	}
	return paths.plistPath, nil
}

func uninstallScheduleAgent(deps daemonAgentDeps) (string, error) {
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths := scheduleAgentPaths(home)
	_ = deps.run("bootout", "gui/"+deps.uid(), paths.plistPath)
	if err := deps.remove(paths.plistPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove LaunchAgent plist: %w", err)
	}
	return paths.plistPath, nil
}

// scheduleAgentStatus returns the launchctl print output and whether the agent
// is loaded. A "service not found" result means simply not-loaded (loaded=false,
// err=nil); any other launchctl failure is a real error.
func scheduleAgentStatus(deps daemonAgentDeps) ([]byte, bool, error) {
	out, err := deps.output("print", "gui/"+deps.uid()+"/"+scheduleAgentLabel)
	if err != nil {
		if isServiceNotFound(out, err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("launchctl print: %w", err)
	}
	return out, true, nil
}

func isServiceNotFound(out []byte, err error) bool {
	blob := strings.ToLower(string(out) + " " + err.Error())
	return strings.Contains(blob, "could not find") || strings.Contains(blob, "no such process")
}

func snapshotLaunchAgentPlist(executable string, intervalSec int, stdoutPath, stderrPath string) string {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	writePlistKeyString(&b, "Label", scheduleAgentLabel)
	b.WriteString("    <key>ProgramArguments</key>\n")
	b.WriteString("    <array>\n")
	writePlistString(&b, executable)
	writePlistString(&b, "snapshot")
	b.WriteString("    </array>\n")
	b.WriteString("    <key>StartInterval</key>\n")
	fmt.Fprintf(&b, "    <integer>%d</integer>\n", intervalSec)
	b.WriteString("    <key>RunAtLoad</key>\n")
	b.WriteString("    <true/>\n")
	writePlistKeyString(&b, "StandardOutPath", stdoutPath)
	writePlistKeyString(&b, "StandardErrorPath", stderrPath)
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}
