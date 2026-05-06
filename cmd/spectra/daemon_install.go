package main

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/kaeawc/spectra/internal/fsutil"
	"github.com/kaeawc/spectra/internal/serve"
)

const daemonAgentLabel = "dev.spectra.daemon"

type daemonAgentOptions struct {
	SockPath       string
	TCPAddr        string
	AllowRemote    bool
	TsnetEnabled   bool
	TsnetAddr      string
	TsnetHostname  string
	TsnetStateDir  string
	TsnetEphemeral bool
	TsnetTags      string
	LogFile        string
	NoLogFile      bool
	NoLoad         bool
}

type daemonAgentDeps struct {
	executable func() (string, error)
	homeDir    func() (string, error)
	uid        func() string
	mkdirAll   func(string, os.FileMode) error
	writeFile  func(string, []byte, os.FileMode) error
	remove     func(string) error
	run        func(args ...string) error
	output     func(args ...string) ([]byte, error)
}

func runInstallDaemonCmd(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "uninstall":
			return runUninstallDaemon(args[1:])
		case "status":
			return runDaemonStatus(args[1:])
		case "print-plist":
			return runPrintDaemonPlist(args[1:])
		}
	}
	return runInstallDaemon(args)
}

func runInstallDaemon(args []string) int {
	opts, ok := parseDaemonAgentOptions("spectra install-daemon", args, os.Stderr)
	if !ok {
		return 2
	}
	plistPath, err := installDaemonAgent(opts, defaultDaemonAgentDeps())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "spectra daemon LaunchAgent installed at %s\n", plistPath)
	if opts.NoLoad {
		fmt.Fprintln(os.Stdout, "Load skipped; run without --no-load to bootstrap it with launchd.")
	}
	return 0
}

func runPrintDaemonPlist(args []string) int {
	opts, ok := parseDaemonAgentOptions("spectra install-daemon print-plist", args, os.Stderr)
	if !ok {
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	paths := daemonAgentPaths(home)
	fmt.Fprint(os.Stdout, daemonLaunchAgentPlist(exe, opts.serveArgs(), paths.stdoutPath, paths.stderrPath))
	return 0
}

func runUninstallDaemon(args []string) int {
	fs := flag.NewFlagSet("spectra install-daemon uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra install-daemon uninstall")
		return 2
	}
	plistPath, err := uninstallDaemonAgent(defaultDaemonAgentDeps())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "spectra daemon LaunchAgent removed from %s\n", plistPath)
	return 0
}

func runDaemonStatus(args []string) int {
	fs := flag.NewFlagSet("spectra install-daemon status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra install-daemon status")
		return 2
	}
	out, err := daemonAgentStatus(defaultDaemonAgentDeps())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprint(os.Stdout, string(out))
	return 0
}

func parseDaemonAgentOptions(name string, args []string, stderr io.Writer) (daemonAgentOptions, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := daemonAgentOptions{}
	fs.StringVar(&opts.SockPath, "sock", "", "Unix socket path passed to spectra serve")
	fs.StringVar(&opts.TCPAddr, "tcp", "", "Optional TCP listen address passed to spectra serve")
	fs.BoolVar(&opts.AllowRemote, "allow-remote", false, "Allow non-loopback TCP listen address")
	fs.BoolVar(&opts.TsnetEnabled, "tsnet", false, "Join the tailnet as a managed tsnet node")
	fs.StringVar(&opts.TsnetAddr, "tsnet-addr", serve.DefaultTsnetAddr, "Tailnet listen address for tsnet")
	fs.StringVar(&opts.TsnetHostname, "tsnet-hostname", "", "Tailnet hostname passed to spectra serve")
	fs.StringVar(&opts.TsnetStateDir, "tsnet-state-dir", "", "tsnet state directory passed to spectra serve")
	fs.BoolVar(&opts.TsnetEphemeral, "tsnet-ephemeral", false, "Register the tsnet node as ephemeral")
	fs.StringVar(&opts.TsnetTags, "tsnet-tags", "", "Comma-separated Tailscale tags to advertise")
	fs.StringVar(&opts.LogFile, "log-file", "", "JSONL daemon log path")
	fs.BoolVar(&opts.NoLogFile, "no-log-file", false, "Disable daemon JSONL log file")
	fs.BoolVar(&opts.NoLoad, "no-load", false, "Write plist but do not bootstrap with launchd")
	if err := fs.Parse(args); err != nil {
		return daemonAgentOptions{}, false
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: spectra install-daemon [--sock path] [--tcp addr] [--allow-remote] [--tsnet] [--tsnet-hostname name] [--log-file path|--no-log-file] [--no-load]")
		return daemonAgentOptions{}, false
	}
	if opts.LogFile != "" && opts.NoLogFile {
		fmt.Fprintln(stderr, "install-daemon: --log-file and --no-log-file are mutually exclusive")
		return daemonAgentOptions{}, false
	}
	if err := validateServeListen(opts.TCPAddr, opts.AllowRemote); err != nil {
		fmt.Fprintln(stderr, err)
		return daemonAgentOptions{}, false
	}
	return opts, true
}

func (o daemonAgentOptions) serveArgs() []string {
	args := []string{"serve"}
	if o.SockPath != "" {
		args = append(args, "--sock", o.SockPath)
	}
	if o.TCPAddr != "" {
		args = append(args, "--tcp", o.TCPAddr)
	}
	if o.AllowRemote {
		args = append(args, "--allow-remote")
	}
	if o.TsnetEnabled {
		args = append(args, "--tsnet")
	}
	if o.TsnetAddr != "" && o.TsnetAddr != serve.DefaultTsnetAddr {
		args = append(args, "--tsnet-addr", o.TsnetAddr)
	}
	if o.TsnetHostname != "" {
		args = append(args, "--tsnet-hostname", o.TsnetHostname)
	}
	if o.TsnetStateDir != "" {
		args = append(args, "--tsnet-state-dir", o.TsnetStateDir)
	}
	if o.TsnetEphemeral {
		args = append(args, "--tsnet-ephemeral")
	}
	if o.TsnetTags != "" {
		args = append(args, "--tsnet-tags", o.TsnetTags)
	}
	if o.LogFile != "" {
		args = append(args, "--log-file", o.LogFile)
	}
	if o.NoLogFile {
		args = append(args, "--no-log-file")
	}
	return args
}

type daemonAgentPathSet struct {
	launchAgentsDir string
	logDir          string
	plistPath       string
	stdoutPath      string
	stderrPath      string
}

func daemonAgentPaths(home string) daemonAgentPathSet {
	return daemonAgentPathSet{
		launchAgentsDir: filepath.Join(home, "Library", "LaunchAgents"),
		logDir:          filepath.Join(home, "Library", "Logs", "Spectra"),
		plistPath:       filepath.Join(home, "Library", "LaunchAgents", daemonAgentLabel+".plist"),
		stdoutPath:      filepath.Join(home, "Library", "Logs", "Spectra", "daemon.launchd.out.log"),
		stderrPath:      filepath.Join(home, "Library", "Logs", "Spectra", "daemon.launchd.err.log"),
	}
}

func installDaemonAgent(opts daemonAgentOptions, deps daemonAgentDeps) (string, error) {
	exe, err := deps.executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths := daemonAgentPaths(home)
	if err := deps.mkdirAll(paths.launchAgentsDir, 0o700); err != nil {
		return "", fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := deps.mkdirAll(paths.logDir, 0o700); err != nil {
		return "", fmt.Errorf("create daemon log directory: %w", err)
	}
	plist := daemonLaunchAgentPlist(exe, opts.serveArgs(), paths.stdoutPath, paths.stderrPath)
	if err := deps.writeFile(paths.plistPath, []byte(plist), 0o644); err != nil {
		return "", fmt.Errorf("write LaunchAgent plist: %w", err)
	}
	if opts.NoLoad {
		return paths.plistPath, nil
	}
	domain := "gui/" + deps.uid()
	_ = deps.run("bootout", domain, paths.plistPath)
	if err := deps.run("bootstrap", domain, paths.plistPath); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %w", err)
	}
	service := domain + "/" + daemonAgentLabel
	if err := deps.run("enable", service); err != nil {
		return "", fmt.Errorf("launchctl enable: %w", err)
	}
	if err := deps.run("kickstart", "-k", service); err != nil {
		return "", fmt.Errorf("launchctl kickstart: %w", err)
	}
	return paths.plistPath, nil
}

func uninstallDaemonAgent(deps daemonAgentDeps) (string, error) {
	home, err := deps.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	paths := daemonAgentPaths(home)
	_ = deps.run("bootout", "gui/"+deps.uid(), paths.plistPath)
	if err := deps.remove(paths.plistPath); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove LaunchAgent plist: %w", err)
	}
	return paths.plistPath, nil
}

func daemonAgentStatus(deps daemonAgentDeps) ([]byte, error) {
	out, err := deps.output("print", "gui/"+deps.uid()+"/"+daemonAgentLabel)
	if err != nil {
		return nil, fmt.Errorf("launchctl print: %w", err)
	}
	return out, nil
}

func defaultDaemonAgentDeps() daemonAgentDeps {
	return daemonAgentDeps{
		executable: os.Executable,
		homeDir:    os.UserHomeDir,
		uid: func() string {
			return strconv.Itoa(os.Getuid())
		},
		mkdirAll:  os.MkdirAll,
		writeFile: fsutil.WriteFileAtomic,
		remove:    os.Remove,
		run:       runLaunchctl,
		output:    outputLaunchctl,
	}
}

func runLaunchctl(args ...string) error {
	// #nosec G204 -- command is fixed to launchctl; args are generated by this installer, no shell.
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func outputLaunchctl(args ...string) ([]byte, error) {
	// #nosec G204 -- command is fixed to launchctl; args are generated by this installer, no shell.
	return exec.Command("launchctl", args...).CombinedOutput()
}

func daemonLaunchAgentPlist(executable string, serveArgs []string, stdoutPath string, stderrPath string) string {
	args := append([]string{executable}, serveArgs...)
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	writePlistKeyString(&b, "Label", daemonAgentLabel)
	b.WriteString("    <key>ProgramArguments</key>\n")
	b.WriteString("    <array>\n")
	for _, arg := range args {
		writePlistString(&b, arg)
	}
	b.WriteString("    </array>\n")
	b.WriteString("    <key>RunAtLoad</key>\n")
	b.WriteString("    <true/>\n")
	b.WriteString("    <key>KeepAlive</key>\n")
	b.WriteString("    <true/>\n")
	writePlistKeyString(&b, "StandardOutPath", stdoutPath)
	writePlistKeyString(&b, "StandardErrorPath", stderrPath)
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

func writePlistKeyString(b *bytes.Buffer, key string, value string) {
	b.WriteString("    <key>")
	_ = xml.EscapeText(b, []byte(key))
	b.WriteString("</key>\n")
	writePlistString(b, value)
}

func writePlistString(b *bytes.Buffer, value string) {
	b.WriteString("        <string>")
	_ = xml.EscapeText(b, []byte(value))
	b.WriteString("</string>\n")
}
