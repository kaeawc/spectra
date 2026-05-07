package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func runRemoteCommand(args globalRemoteArgs) int {
	target, err := parseConnectTarget(args.target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	method, params, err := parseConnectCall(args.args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage: spectra --remote <target> <subcommand> [args]")
		return 2
	}
	conn, err := dialConnectTarget(target, args.timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "connect %s: %v\n", args.target, err)
		return 1
	}
	defer conn.Close()
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
