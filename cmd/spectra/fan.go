package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kaeawc/spectra/internal/store"
)

type fanRPCCaller func(target connectTarget, timeout time.Duration, method string, params json.RawMessage) (json.RawMessage, error)
type fanHostLister func(ctx context.Context, probeUnreachable bool) ([]fanTarget, error)
type fanTargetProber func(ctx context.Context, target connectTarget, timeout time.Duration) error

type fanOutput struct {
	Method  string            `json:"method"`
	Targets []fanTargetOutput `json:"targets"`
}

type fanTargetOutput struct {
	Target  string          `json:"target"`
	Network string          `json:"network"`
	Address string          `json:"address"`
	OK      bool            `json:"ok"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func runFan(args []string) int {
	return runFanWith(args, os.Stdout, os.Stderr, callFanRPC, discoverFanTargets)
}

func runFanWith(args []string, stdout io.Writer, stderr io.Writer, caller fanRPCCaller, discover fanHostLister) int {
	fs := flag.NewFlagSet("spectra fan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	timeout := fs.Duration("timeout", 3*time.Second, "Dial/read timeout per target")
	hosts := fs.String("hosts", "", "Comma-separated daemon targets; defaults to discovered hosts")
	probe := fs.Bool("probe", false, "When discovering hosts, skip entries that fail a health check")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var targets []fanTarget
	var err error
	if strings.TrimSpace(*hosts) == "" {
		targets, err = discover(context.Background(), *probe)
	} else {
		targets, err = parseFanTargets(*hosts)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		printFanUsage(stderr)
		return 2
	}
	method, params, err := parseConnectCall(fs.Args())
	if err != nil {
		fmt.Fprintln(stderr, err)
		printFanUsage(stderr)
		return 2
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "fan requires --hosts target[,target...]")
		printFanUsage(stderr)
		return 2
	}

	results := fanCallTargets(targets, *timeout, method, params, caller)
	out := fanOutput{Method: method, Targets: results}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(stderr, "encode fan response: %v\n", err)
		return 1
	}
	for _, result := range results {
		if !result.OK {
			return 1
		}
	}
	return 0
}

func printFanUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: spectra fan [--hosts host-a,host-b] [--probe] [status|host|jvm|processes|network|storage|power|rules]")
	fmt.Fprintln(w, "   or: spectra fan [--hosts host-a,host-b] inspect <App.app>")
	fmt.Fprintln(w, "   or: spectra fan [--hosts host-a,host-b] call <method> [json-params]")
}

func parseFanTargets(raw string) ([]fanTarget, error) {
	parts := strings.Split(raw, ",")
	targets := make([]fanTarget, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		target, err := parseConnectTarget(name)
		if err != nil {
			return nil, fmt.Errorf("parse fan target %q: %w", name, err)
		}
		targets = append(targets, fanTarget{Name: name, Target: target})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("fan requires --hosts target[,target...]")
	}
	return targets, nil
}

var fanProbeTarget fanTargetProber = probeFanTarget

func discoverFanTargets(ctx context.Context, probeUnreachable bool) ([]fanTarget, error) {
	dbPath, err := store.DefaultPath()
	if err != nil {
		return nil, err
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(rows))
	targets := make([]fanTarget, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Hostname)
		if name == "" {
			continue
		}
		target, err := parseConnectTarget(net.JoinHostPort(name, defaultRemotePort))
		if err != nil {
			continue
		}
		key := target.Address
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, fanTarget{Name: name, Target: target})
	}
	if probeUnreachable {
		filtered := make([]fanTarget, 0, len(targets))
		for _, target := range targets {
			if err := fanProbeTarget(ctx, target.Target, 3*time.Second); err != nil {
				continue
			}
			filtered = append(filtered, target)
		}
		targets = filtered
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("fan requires --hosts target[,target...]")
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Name < targets[j].Name
	})
	return targets, nil
}

func probeFanTarget(ctx context.Context, target connectTarget, timeout time.Duration) error {
	conn, err := dialConnectTarget(target, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()

	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(timeout))
	}
	_, err = callRPC(conn, "health", nil)
	return err
}

type fanTarget struct {
	Name   string
	Target connectTarget
}

func fanCallTargets(targets []fanTarget, timeout time.Duration, method string, params json.RawMessage, caller fanRPCCaller) []fanTargetOutput {
	results := make([]fanTargetOutput, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))
	for i, target := range targets {
		go func(i int, target fanTarget) {
			defer wg.Done()
			result, err := caller(target.Target, timeout, method, params)
			results[i] = fanTargetOutput{
				Target:  target.Name,
				Network: target.Target.Network,
				Address: target.Target.Address,
				OK:      err == nil,
				Result:  result,
			}
			if err != nil {
				results[i].Error = err.Error()
			}
		}(i, target)
	}
	wg.Wait()
	return results
}

func callFanRPC(target connectTarget, timeout time.Duration, method string, params json.RawMessage) (json.RawMessage, error) {
	conn, err := dialConnectTarget(target, timeout)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", target.Address, err)
	}
	defer conn.Close()

	if d, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = d.SetDeadline(time.Now().Add(timeout))
	}
	return callRPC(conn, method, params)
}
