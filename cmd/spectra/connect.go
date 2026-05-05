package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/serve"
)

const defaultRemotePort = "7878"

type connectTarget struct {
	Network string
	Address string
}

func runConnect(args []string) int {
	fs := flag.NewFlagSet("spectra connect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	timeout := fs.Duration("timeout", 3*time.Second, "Dial/read timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		printConnectUsage(os.Stderr)
		return 2
	}

	target, err := parseConnectTarget(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	method, params, err := parseConnectCall(fs.Args()[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printConnectUsage(os.Stderr)
		return 2
	}

	conn, err := dialConnectTarget(target, *timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", fs.Arg(0), err)
		return 1
	}
	defer conn.Close()

	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(*timeout))
	}

	result, err := callRPC(conn, method, params)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "encode response: %v\n", err)
		return 1
	}
	return 0
}

func printConnectUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: spectra connect [--timeout 3s] <target> [status|health|jvm|processes|network|toolchains]")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> inspect <App.app>")
	fmt.Fprintln(w, "   or: spectra connect [--timeout 3s] <target> call <method> [json-params]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "targets: local, unix:/path/to/sock, /path/to/sock, host:port, host")
}

func parseConnectTarget(raw string) (connectTarget, error) {
	switch {
	case raw == "local":
		sock, err := serve.DefaultSockPath()
		if err != nil {
			return connectTarget{}, fmt.Errorf("resolve local socket: %w", err)
		}
		return connectTarget{Network: "unix", Address: sock}, nil
	case strings.HasPrefix(raw, "unix:"):
		path := strings.TrimPrefix(raw, "unix:")
		if path == "" {
			return connectTarget{}, fmt.Errorf("empty unix socket target")
		}
		return connectTarget{Network: "unix", Address: path}, nil
	case strings.HasPrefix(raw, "/"):
		return connectTarget{Network: "unix", Address: raw}, nil
	}
	if _, _, err := net.SplitHostPort(raw); err == nil {
		return connectTarget{Network: "tcp", Address: raw}, nil
	}
	return connectTarget{Network: "tcp", Address: net.JoinHostPort(raw, defaultRemotePort)}, nil
}

func parseConnectCall(args []string) (string, json.RawMessage, error) {
	if len(args) == 0 || args[0] == "status" || args[0] == "health" {
		return "health", nil, nil
	}
	if method, params, ok, err := parseConnectShortcut(args); ok || err != nil {
		return method, params, err
	}
	return parseConnectGenericCall(args)
}

func parseConnectShortcut(args []string) (string, json.RawMessage, bool, error) {
	shortcuts := map[string]string{
		"jvm":        "jvm.list",
		"process":    "process.list",
		"processes":  "process.list",
		"network":    "network.state",
		"toolchain":  "toolchain.scan",
		"toolchains": "toolchain.scan",
	}
	if method, ok := shortcuts[args[0]]; ok {
		if len(args) != 1 {
			return "", nil, true, fmt.Errorf("connect %s takes no extra arguments", args[0])
		}
		return method, nil, true, nil
	}
	if args[0] != "inspect" {
		return "", nil, false, nil
	}
	if len(args) != 2 {
		return "", nil, true, fmt.Errorf("connect inspect requires <App.app>")
	}
	params, _ := json.Marshal(map[string]string{"path": args[1]})
	return "inspect.app", json.RawMessage(params), true, nil
}

func parseConnectGenericCall(args []string) (string, json.RawMessage, error) {
	if args[0] != "call" || len(args) < 2 || len(args) > 3 {
		return "", nil, fmt.Errorf("invalid connect command")
	}
	if len(args) == 2 {
		return args[1], nil, nil
	}
	var params json.RawMessage
	if err := json.Unmarshal([]byte(args[2]), &params); err != nil {
		return "", nil, fmt.Errorf("invalid json params: %w", err)
	}
	return args[1], params, nil
}

func dialConnectTarget(target connectTarget, timeout time.Duration) (io.ReadWriteCloser, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, target.Network, target.Address)
}

func callRPC(rw io.ReadWriter, method string, params json.RawMessage) (json.RawMessage, error) {
	req := rpc.Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  method,
		Params:  params,
	}
	if len(req.Params) == 0 {
		req.Params = nil
	}
	if err := json.NewEncoder(rw).Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result,omitempty"`
		Error   *rpc.RPCError   `json:"error,omitempty"`
	}
	if err := json.NewDecoder(rw).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("daemon error: %s", resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return json.RawMessage(`null`), nil
	}
	return resp.Result, nil
}
