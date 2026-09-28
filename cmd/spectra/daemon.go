package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/logger"
)

const daemonAgentLabel = "dev.spectra.daemon"
const daemonUnitName = "spectra-daemon.service"

func runDaemon(args []string) int {
	return runDaemonWithIO(args, os.Stdout, os.Stderr, defaultDaemonDeps())
}

func runDaemonWithIO(args []string, out, stderr io.Writer, deps daemonDeps) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: spectra daemon <run|start|stop|status|install|uninstall|print-plist|logs>")
		return 2
	}
	if len(args) > 1 && daemonNoArgs(args[0]) {
		fmt.Fprintf(stderr, "daemon %s takes no arguments\n", args[0])
		return 2
	}
	paths, err := deps.paths()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var actionErr error
	switch args[0] {
	case "run":
		actionErr = daemonRun(args[1:], paths, stderr)
	case "start":
		actionErr = daemonStart(paths, out, deps)
	case "stop":
		actionErr = daemonStop(paths, out, deps)
	case "status":
		actionErr = daemonStatus(args[1:], paths, out)
	case "install":
		actionErr = daemonInstall(paths, out, deps)
	case "uninstall":
		actionErr = daemonUninstall(out, deps)
	case "print-plist":
		actionErr = daemonPrintPlist(out, deps)
	case "logs":
		actionErr = daemonLogs(args[1:], paths, out, deps)
	default:
		fmt.Fprintf(stderr, "unknown daemon subcommand %q\n", args[0])
		return 2
	}
	if actionErr != nil {
		fmt.Fprintln(stderr, actionErr)
		return 1
	}
	return 0
}

func daemonNoArgs(command string) bool {
	switch command {
	case "start", "stop", "install", "uninstall", "print-plist":
		return true
	default:
		return false
	}
}

func daemonRun(args []string, paths daemon.Paths, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	idle := fs.Duration("idle-timeout", 0, "stop after inactivity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *idle < 0 {
		return fmt.Errorf("usage: spectra daemon run [--idle-timeout duration]")
	}
	if err := os.MkdirAll(filepath.Dir(paths.Log), 0o700); err != nil {
		return fmt.Errorf("daemon log directory: %w", err)
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("daemon log: %w", err)
	}
	defer logFile.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, daemonTermSignal)
	defer stop()
	return daemon.New(daemon.Options{Paths: paths, Version: version, Logger: logger.New(logger.Config{Writer: logFile, Format: logger.FormatJSON, Level: slog.LevelInfo}), IdleTimeout: *idle}).Run(ctx)
}

func daemonStart(paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	if c, ok := daemonclient.Discover(paths); ok {
		defer c.Close()
		st, err := c.Status(context.Background())
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "already running (pid %d, socket %s)\n", st.PID, st.Socket)
		return nil
	}
	exe, err := deps.executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	c, err := daemonclient.EnsureRunning(ctx, paths, daemonclient.SpawnOptions{Exe: exe, Args: []string{"daemon", "run"}, Wait: 5 * time.Second, LogPath: paths.Log})
	if err != nil {
		return err
	}
	defer c.Close()
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "started (pid %d, socket %s)\n", st.PID, st.Socket)
	return nil
}

func daemonStop(paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	if c, ok := daemonclient.Discover(paths); ok {
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var result struct {
			OK bool `json:"ok"`
		}
		if err := c.Call(ctx, daemon.MethodShutdown, nil, &result); err != nil {
			return err
		}
		if !result.OK {
			return fmt.Errorf("daemon refused shutdown")
		}
		fmt.Fprintln(out, "daemon stopping")
		return nil
	}
	data, err := deps.readFile(paths.PID)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "daemon not running")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read daemon pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return fmt.Errorf("invalid daemon pid file")
	}
	if err := deps.signal(pid, syscall.Signal(0)); err != nil {
		fmt.Fprintln(out, "daemon not running")
		return nil
	}
	if err := deps.signal(pid, daemonTermSignal); err != nil {
		return fmt.Errorf("signal daemon pid %d: %w", pid, err)
	}
	for i := 0; i < 200; i++ {
		if _, err := deps.readFile(paths.PID); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "daemon stopped")
			return nil
		}
		deps.sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("daemon pid %d did not stop within 5s", pid)
}

func daemonStatus(args []string, paths daemon.Paths, out io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: spectra daemon status [--json]")
	}
	c, ok := daemonclient.Discover(paths)
	if !ok {
		return fmt.Errorf("daemon not running")
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if err := daemonclient.CheckVersion(st, version); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(out).Encode(st)
	}
	fmt.Fprintf(out, "daemon running (pid %d, socket %s, version %s)\n", st.PID, st.Socket, st.Version)
	return nil
}

func daemonLogs(args []string, paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	fs := flag.NewFlagSet("daemon logs", flag.ContinueOnError)
	n := fs.Int("n", 50, "number of lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *n < 0 {
		return fmt.Errorf("usage: spectra daemon logs [-n N]")
	}
	data, err := deps.readFile(paths.Log)
	if err != nil {
		return fmt.Errorf("read daemon log: %w", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	if *n == 0 || len(data) == 0 {
		return nil
	}
	if len(lines) > *n {
		lines = lines[len(lines)-*n:]
	}
	_, err = out.Write(append(bytes.Join(lines, []byte{'\n'}), '\n'))
	return err
}

func daemonServicePaths(home, goos string) (string, string, string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", daemonAgentLabel+".plist"), filepath.Join(home, "Library", "Logs", "Spectra", "daemon.launchd.out.log"), filepath.Join(home, "Library", "Logs", "Spectra", "daemon.launchd.err.log"), nil
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", daemonUnitName), "", "", nil
	default:
		return "", "", "", fmt.Errorf("daemon service unsupported on %s", goos)
	}
}

func daemonInstall(paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	path, content, err := daemonServiceContent(deps)
	if err != nil {
		return err
	}
	if err := deps.mkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create service directory: %w", err)
	}
	if deps.goos == "darwin" {
		if err := deps.mkdirAll(filepath.Dir(paths.Log), 0o700); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
	}
	if err := deps.writeFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write service: %w", err)
	}
	if deps.goos == "darwin" {
		domain := fmt.Sprintf("gui/%d", deps.uid())
		if _, err := deps.output("launchctl", "print", domain+"/"+daemonAgentLabel); err == nil {
			_ = deps.run("launchctl", "bootout", domain, path)
		}
		if err := deps.run("launchctl", "bootstrap", domain, path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %w", err)
		}
	} else {
		if err := deps.run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl reload: %w", err)
		}
		if err := deps.run("systemctl", "--user", "enable", "--now", daemonUnitName); err != nil {
			return fmt.Errorf("systemctl enable: %w", err)
		}
	}
	fmt.Fprintf(out, "daemon service installed at %s\n", path)
	return nil
}

func daemonUninstall(out io.Writer, deps daemonDeps) error {
	home, err := deps.home()
	if err != nil {
		return fmt.Errorf("home: %w", err)
	}
	path, _, _, err := daemonServicePaths(home, deps.goos)
	if err != nil {
		return err
	}
	if deps.goos == "darwin" {
		_ = deps.run("launchctl", "bootout", fmt.Sprintf("gui/%d", deps.uid()), path)
	} else {
		_ = deps.run("systemctl", "--user", "disable", "--now", daemonUnitName)
	}
	if err := deps.remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove service: %w", err)
	}
	if deps.goos == "linux" {
		if err := deps.run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl reload: %w", err)
		}
	}
	fmt.Fprintf(out, "daemon service removed from %s\n", path)
	return nil
}

func daemonPrintPlist(out io.Writer, deps daemonDeps) error {
	if deps.goos != "darwin" {
		return fmt.Errorf("print-plist is macOS only")
	}
	_, content, err := daemonServiceContent(deps)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, content)
	return err
}

func daemonServiceContent(deps daemonDeps) (string, string, error) {
	home, err := deps.home()
	if err != nil {
		return "", "", fmt.Errorf("home: %w", err)
	}
	exe, err := deps.executable()
	if err != nil {
		return "", "", fmt.Errorf("executable: %w", err)
	}
	path, stdout, stderr, err := daemonServicePaths(home, deps.goos)
	if err != nil {
		return "", "", err
	}
	if deps.goos == "darwin" {
		return path, daemonPlist(exe, stdout, stderr), nil
	}
	return path, daemonUnit(exe), nil
}

func daemonPlist(exe, stdout, stderr string) string {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n")
	for _, pair := range [][2]string{{"Label", daemonAgentLabel}, {"StandardOutPath", stdout}, {"StandardErrorPath", stderr}, {"ProcessType", "Background"}} {
		fmt.Fprintf(&b, "<key>%s</key><string>", pair[0])
		_ = xml.EscapeText(&b, []byte(pair[1]))
		b.WriteString("</string>\n")
	}
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range []string{exe, "daemon", "run"} {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(arg))
		b.WriteString("</string>")
	}
	b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>Nice</key><integer>10</integer>\n<key>LowPriorityIO</key><true/>\n<key>ThrottleInterval</key><integer>30</integer>\n</dict></plist>\n")
	return b.String()
}

func daemonUnit(exe string) string {
	return fmt.Sprintf("[Unit]\nDescription=Spectra local daemon\n\n[Service]\nType=simple\nExecStart=%s daemon run\nRestart=on-failure\nNice=10\nCPUWeight=20\nIOSchedulingClass=idle\n\n[Install]\nWantedBy=default.target\n", strconv.Quote(exe))
}
