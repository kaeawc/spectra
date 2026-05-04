package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/serve"
)

func runServe(args []string) int {
	fs := flag.NewFlagSet("spectra serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sockPath := fs.String("sock", "", "Unix socket path (default: ~/.spectra/sock)")
	if err := fs.Parse(args); err != nil {
		return 2
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
	fmt.Fprintf(os.Stderr, "spectra serve: listening on %s\n", sock)

	if err := serve.Run(ctx, serve.Options{
		SockPath:       sock,
		SpectraVersion: version,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
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
