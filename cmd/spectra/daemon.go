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
	"strings"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/daemon"
	"github.com/kaeawc/spectra/internal/daemonclient"
	"github.com/kaeawc/spectra/internal/daemoncrash"
	"github.com/kaeawc/spectra/internal/daemonhost"
	"github.com/kaeawc/spectra/internal/daemoninventory"
	"github.com/kaeawc/spectra/internal/daemonjvm"
	"github.com/kaeawc/spectra/internal/daemonprocess"
	"github.com/kaeawc/spectra/internal/daemonprocinfo"
	"github.com/kaeawc/spectra/internal/daemonprofile"
	"github.com/kaeawc/spectra/internal/detect"
	"github.com/kaeawc/spectra/internal/hostos"
	"github.com/kaeawc/spectra/internal/hostwatch"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/store"
)

const daemonAgentLabel = "dev.spectra.daemon"
const daemonUnitName = "spectra-daemon.service"

func runDaemon(args []string) int {
	return runDaemonWithIO(args, os.Stdout, os.Stderr, defaultDaemonDeps())
}

func runDaemonWithIO(args []string, out, stderr io.Writer, deps daemonDeps) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: spectra daemon <run|start|stop|status|install|uninstall|print-plist|logs|profile>")
		return 2
	}
	runArgs, ok := daemonSubcommandArgs(args, stderr)
	if !ok {
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
		actionErr = daemonStart(paths, out, deps, runArgs)
	case "stop":
		actionErr = daemonStop(paths, out, deps)
	case "status":
		actionErr = daemonStatus(args[1:], paths, out, deps)
	case "install":
		actionErr = daemonInstall(paths, out, deps, runArgs)
	case "uninstall":
		actionErr = daemonUninstall(out, deps)
	case "print-plist":
		actionErr = daemonPrintPlist(out, deps, runArgs)
	case "logs":
		actionErr = daemonLogs(args[1:], paths, out, deps)
	case "profile":
		actionErr = daemonProfile(args[1:], paths, out, deps)
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

// daemonSubcommandArgs validates the arguments of subcommands that take
// none or only forwarded run flags, returning the `daemon run` argv for the
// latter. It reports false after printing a usage error.
func daemonSubcommandArgs(args []string, stderr io.Writer) ([]string, bool) {
	if len(args) > 1 && daemonNoArgs(args[0]) {
		fmt.Fprintf(stderr, "daemon %s takes no arguments\n", args[0])
		return nil, false
	}
	if !daemonTakesRunFlags(args[0]) {
		return nil, true
	}
	runArgs, err := daemonRunArgs(args[0], args[1:], stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, false
	}
	return runArgs, true
}

func daemonNoArgs(command string) bool {
	switch command {
	case "stop", "uninstall":
		return true
	default:
		return false
	}
}

func daemonTakesRunFlags(command string) bool {
	switch command {
	case "start", "install", "print-plist":
		return true
	default:
		return false
	}
}

// daemonRunArgs parses the daemon options that start, install and
// print-plist forward, and returns the `daemon run` argv they launch. The
// service files and the spawn command carry the options themselves, so
// launchd/systemd restarts keep them without a separate settings file.
func daemonRunArgs(command string, args []string, stderr io.Writer) ([]string, error) {
	fs := flag.NewFlagSet("daemon "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	assessGatekeeper := fs.Bool("assess-gatekeeper", false, "re-assess installed apps with spctl in the background, one at a time (macOS)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("usage: spectra daemon %s [--assess-gatekeeper]", command)
	}
	runArgs := []string{"daemon", "run"}
	if *assessGatekeeper {
		runArgs = append(runArgs, "--assess-gatekeeper")
	}
	return runArgs, nil
}

func daemonRun(args []string, paths daemon.Paths, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	idle := fs.Duration("idle-timeout", 0, "stop after inactivity")
	watchInterval := fs.Duration("watch-interval", 15*time.Second, "host monitoring interval")
	noNotify := fs.Bool("no-notify", false, "disable desktop notifications")
	assessGatekeeper := fs.Bool("assess-gatekeeper", false, "re-assess installed apps with spctl in the background, one at a time (macOS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *idle < 0 || *watchInterval <= 0 {
		return fmt.Errorf("usage: spectra daemon run [--idle-timeout duration] [--watch-interval duration] [--no-notify] [--assess-gatekeeper]")
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
	log := logger.New(logger.Config{Writer: logFile, Format: logger.FormatJSON, Level: slog.LevelInfo})
	dbPath := os.Getenv("SPECTRA_WATCH_DB")
	if dbPath == "" {
		dbPath, err = store.DefaultPath()
		if err != nil {
			return fmt.Errorf("watch database path: %w", err)
		}
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("watch database: %w", err)
	}
	defer db.Close()
	cfg, err := hostwatch.LoadConfig(filepath.Join(paths.Dir, "watch.yml"), nil)
	if err != nil {
		log.Warn("watch config invalid; using defaults", "error", err)
	}
	processMetrics := metrics.NewCollector()
	var notifier hostwatch.Notifier = hostwatch.NoopNotifier{}
	if !*noNotify {
		notifier = &hostwatch.RateLimitedNotifier{Inner: hostwatch.DesktopNotifier{}}
	}
	watch := hostwatch.NewService(hostwatch.ServiceOptions{Store: db, Metrics: processMetrics, Collector: &hostwatch.Collector{Metrics: processMetrics, Logger: log}, Config: cfg, Interval: *watchInterval, Logger: log, Notifier: notifier})
	server := daemon.New(daemon.Options{Paths: paths, Version: version, Logger: log, IdleTimeout: *idle})
	hostwatch.RegisterMethods(server, watch)
	jvmService := daemonjvm.NewService(daemonjvm.Options{Snapshot: daemonjvm.DefaultSnapshot(version)})
	daemonjvm.RegisterMethods(server, jvmService)
	daemonjvm.RegisterReadMethods(server, jvmService)
	daemonprocess.RegisterMethods(server, daemonprocess.NewService(daemonprocess.Options{Interval: *watchInterval}))
	daemonhost.RegisterMethods(server, daemonhost.NewService(daemonhost.Options{Whatswrong: daemonhost.DefaultWhatswrong(watch)}))
	daemoncrash.RegisterMethods(server, daemoncrash.NewService(daemoncrash.Options{}))
	daemonhost.RegisterReports(server, daemonhost.NewReports())
	daemoninventory.RegisterMethods(server, daemoninventory.NewService(daemoninventory.Options{Snapshots: db, Live: daemoninventory.DefaultLive(version), Logger: log}))
	daemonprocinfo.RegisterMethods(server, daemonprocinfo.NewService(daemonprocinfo.Options{}))
	profiles := daemonprofile.NewService(daemonprofile.Options{})
	defer profiles.Close(5 * time.Second)
	// Jobs outlive their client connections, so cancel them as soon as
	// shutdown starts rather than after the connections drain.
	server.OnShutdown(profiles.Cancel)
	daemonprofile.RegisterMethods(server, profiles)
	watchCtx, cancelWatch := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		if err := watch.Run(watchCtx); err != nil {
			log.Error("watch stopped", "error", err)
		}
	}()
	stopGatekeeper := startGatekeeperRefresh(ctx, *assessGatekeeper, log)
	err = server.Run(ctx)
	cancelWatch()
	stopGatekeeper()
	<-watchDone
	return err
}

// startGatekeeperRefresh runs the background spctl refresher when enabled
// (opt-in, like --assess-gatekeeper on inspect) and returns a func that
// stops it and waits for the in-flight assessment to end.
func startGatekeeperRefresh(ctx context.Context, enabled bool, log logger.Logger) func() {
	if !enabled || hostos.Current() != hostos.Darwin {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	r := daemon.NewGatekeeperRefresher(daemon.GatekeeperRefreshOptions{
		Apps: func() []string {
			return append(scanAppDir("/Applications"), scanAppDir("/Applications/Utilities")...)
		},
		Detect: detect.Options{SealCache: sealCache()},
		Logger: log,
	})
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	return func() { cancel(); <-done }
}

func daemonStart(paths daemon.Paths, out io.Writer, deps daemonDeps, runArgs []string) error {
	if c, ok := deps.discover(paths); ok {
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
	c, err := deps.ensureRunning(ctx, paths, daemonclient.SpawnOptions{Exe: exe, Args: runArgs, Wait: 5 * time.Second, LogPath: paths.Log})
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
	if c, ok := deps.discover(paths); ok {
		return daemonStopRPC(c, out)
	}
	running, pid, err := deps.probe(paths)
	if err != nil {
		return fmt.Errorf("probe daemon: %w", err)
	}
	if !running {
		fmt.Fprintln(out, "daemon not running (cleaned stale files)")
		return nil
	}
	if pid == 0 {
		return daemonStopUnknownPID(paths, out, deps)
	}
	return daemonStopPID(paths, out, deps, pid)
}

func daemonStopUnknownPID(paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	for i := 0; i < 200; i++ {
		deps.sleep(25 * time.Millisecond)
		if c, ok := deps.discover(paths); ok {
			return daemonStopRPC(c, out)
		}
		running, pid, err := deps.probe(paths)
		if err != nil {
			return fmt.Errorf("probe daemon: %w", err)
		}
		if !running {
			fmt.Fprintln(out, "daemon stopped")
			return nil
		}
		if pid != 0 {
			return daemonStopPID(paths, out, deps, pid)
		}
	}
	return fmt.Errorf("daemon still starting; PID unavailable after 5s")
}

func daemonStopPID(paths daemon.Paths, out io.Writer, deps daemonDeps, pid int) error {
	if err := deps.signal(pid, syscall.Signal(0)); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			fmt.Fprintln(out, "daemon not running")
			return nil
		}
		return fmt.Errorf("check daemon pid %d: %w", pid, err)
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

func daemonStopRPC(c *daemonclient.Client, out io.Writer) error {
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

func daemonStatus(args []string, paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: spectra daemon status [--json]")
	}
	c, ok := deps.discover(paths)
	if !ok {
		running, pid, err := deps.probe(paths)
		if err != nil {
			return fmt.Errorf("probe daemon: %w", err)
		}
		if running && pid == 0 {
			return fmt.Errorf("daemon starting (PID unavailable)")
		}
		if running {
			return fmt.Errorf("daemon running (pid %d), socket unavailable", pid)
		}
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

// daemonProfile asks the running daemon for one runtime profile; the
// daemon writes it under its own directory and reports the path.
func daemonProfile(args []string, paths daemon.Paths, out io.Writer, deps daemonDeps) error {
	fs := flag.NewFlagSet("daemon profile", flag.ContinueOnError)
	kind := fs.String("kind", "heap", "heap, allocs, goroutine or cpu")
	seconds := fs.Int("seconds", 0, "CPU profile duration (kind cpu; default 10, max 120)")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: spectra daemon profile [--kind heap|allocs|goroutine|cpu] [--seconds N] [--json]")
	}
	c, ok := deps.discover(paths)
	if !ok {
		return fmt.Errorf("daemon not running")
	}
	defer c.Close()
	wait := 10 * time.Second
	if *kind == "cpu" {
		wait += 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	var result daemon.ProfileResult
	if err := c.Call(ctx, daemon.MethodProfile, daemon.ProfileParams{Kind: *kind, Seconds: *seconds}, &result); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "%s profile written to %s (heap in use %.1f MiB, %d goroutines)\n", result.Kind, result.Path, float64(result.HeapInuse)/(1<<20), result.NumGoroutine)
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

func daemonInstall(paths daemon.Paths, out io.Writer, deps daemonDeps, runArgs []string) error {
	path, content, err := daemonServiceContent(deps, runArgs)
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

func daemonPrintPlist(out io.Writer, deps daemonDeps, runArgs []string) error {
	if deps.goos != "darwin" {
		return fmt.Errorf("print-plist is macOS only")
	}
	_, content, err := daemonServiceContent(deps, runArgs)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, content)
	return err
}

func daemonServiceContent(deps daemonDeps, runArgs []string) (string, string, error) {
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
		return path, daemonPlist(exe, stdout, stderr, runArgs), nil
	}
	return path, daemonUnit(exe, runArgs), nil
}

func daemonPlist(exe, stdout, stderr string, runArgs []string) string {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict>\n")
	for _, pair := range [][2]string{{"Label", daemonAgentLabel}, {"StandardOutPath", stdout}, {"StandardErrorPath", stderr}, {"ProcessType", "Background"}} {
		fmt.Fprintf(&b, "<key>%s</key><string>", pair[0])
		_ = xml.EscapeText(&b, []byte(pair[1]))
		b.WriteString("</string>\n")
	}
	b.WriteString("<key>ProgramArguments</key><array>")
	for _, arg := range append([]string{exe}, runArgs...) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(arg))
		b.WriteString("</string>")
	}
	b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>Nice</key><integer>10</integer>\n<key>LowPriorityIO</key><true/>\n<key>ThrottleInterval</key><integer>30</integer>\n</dict></plist>\n")
	return b.String()
}

func daemonUnit(exe string, runArgs []string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(exe)
	return fmt.Sprintf("[Unit]\nDescription=Spectra local daemon\n\n[Service]\nType=simple\nExecStart=\"%s\" %s\nRestart=on-failure\nNice=10\nCPUWeight=20\nIOSchedulingClass=idle\n\n[Install]\nWantedBy=default.target\n", escaped, strings.Join(runArgs, " "))
}
