package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/serve"
)

func runServe(args []string) int {
	fs := flag.NewFlagSet("spectra serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sockPath := fs.String("sock", "", "Unix socket path (default: ~/.spectra/sock)")
	tcpAddr := fs.String("tcp", "", "Optional TCP listen address, such as 127.0.0.1:7878")
	allowRemote := fs.Bool("allow-remote", false, "Allow --tcp to bind a non-loopback address")
	logFile := fs.String("log-file", "", "JSONL daemon log path (default: ~/Library/Logs/Spectra/daemon.jsonl)")
	noLogFile := fs.Bool("no-log-file", false, "Disable the daemon JSONL log file")
	daemon := fs.Bool("daemon", false, "Start spectra serve in the background and return")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateServeListen(*tcpAddr, *allowRemote); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *daemon {
		if err := startDetachedServeFunc(serveChildArgs(args)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "spectra serve: started in background")
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sock := *sockPath
	if sock == "" {
		var err error
		sock, err = serve.DefaultSockPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	daemonLog, closeLog, resolvedLogPath, err := openDaemonLogger(*logFile, *noLogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if closeLog != nil {
		defer closeLog()
	}
	fmt.Fprintf(os.Stderr, "spectra serve: listening on %s\n", sock)
	if resolvedLogPath != "" {
		fmt.Fprintf(os.Stderr, "spectra serve: logging to %s\n", resolvedLogPath)
	}
	if *tcpAddr != "" {
		if *allowRemote {
			fmt.Fprintln(os.Stderr, "spectra serve: warning: TCP RPC has no Spectra-layer authentication; rely on SSH/Tailscale/firewall controls")
		}
		fmt.Fprintf(os.Stderr, "spectra serve: listening on tcp %s\n", *tcpAddr)
	}

	var detectStore *cache.ShardedStore
	if cacheStores != nil {
		detectStore = cacheStores.Detect
	}
	if err := serve.Run(ctx, serve.Options{
		SockPath:       sock,
		TCPAddr:        *tcpAddr,
		SpectraVersion: version,
		DetectStore:    detectStore,
		Logger:         daemonLog,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func validateServeListen(tcpAddr string, allowRemote bool) error {
	if tcpAddr != "" && !allowRemote && !isLoopbackListenAddr(tcpAddr) {
		return fmt.Errorf("spectra serve: --tcp is limited to loopback unless --allow-remote is set")
	}
	return nil
}

var startDetachedServeFunc = startDetachedServe

func serveChildArgs(args []string) []string {
	child := []string{"serve"}
	for _, arg := range args {
		switch arg {
		case "--daemon", "-daemon", "--daemon=true", "-daemon=true":
			continue
		default:
			child = append(child, arg)
		}
	}
	return child
}

func startDetachedServe(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer null.Close()
	// #nosec G204 -- restarts this executable with parsed serve flags, no shell.
	cmd := exec.Command(exe, args...)
	cmd.Stdin = null
	cmd.Stdout = null
	cmd.Stderr = null
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start detached spectra serve: %w", err)
	}
	return nil
}

func openDaemonLogger(path string, disabled bool) (logger.Logger, func(), string, error) {
	if disabled {
		return nil, nil, "", nil
	}
	if path == "" {
		var err error
		path, err = serve.DefaultLogPath()
		if err != nil {
			return nil, nil, "", err
		}
	}
	f, err := serve.OpenLogFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	log := logger.New(logger.Config{Writer: f, Format: logger.FormatJSON})
	return log, func() { _ = f.Close() }, path, nil
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// runStatus sends a health request to the running daemon and prints the result.
func runStatus(args []string) int {
	fs := flag.NewFlagSet("spectra status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sockPath := fs.String("sock", "", "Unix socket path (default: ~/.spectra/sock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	sock := *sockPath
	if sock == "" {
		var err error
		sock, err = serve.DefaultSockPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	conn, err := rpc.DialUnix(sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "daemon not running (could not connect to %s): %v\n", sock, err)
		return 1
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 1, Method: "health"})

	// Set a short read deadline so status doesn't hang if daemon is stuck.
	type deadliner interface{ SetDeadline(time.Time) error }
	if d, ok := conn.(deadliner); ok {
		_ = d.SetDeadline(time.Now().Add(3 * time.Second))
	}

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		fmt.Fprintf(os.Stderr, "error reading daemon response: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "daemon error: %s\n", resp.Error.Message)
		return 1
	}

	out, _ := json.MarshalIndent(resp.Result, "", "  ")
	fmt.Println(string(out))
	return 0
}
