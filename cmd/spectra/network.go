package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/netstate"
)

func runNetwork(args []string) int {
	if len(args) > 0 && args[0] == "connections" {
		return runNetworkConnections(args[1:])
	}
	if len(args) > 0 && args[0] == "firewall" {
		return runNetworkFirewall(args[1:])
	}

	fs := flag.NewFlagSet("spectra network", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	state := netstate.Collect(netstate.DefaultRunner)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(state)
		return 0
	}

	printNetworkState(state)
	return 0
}

func runNetworkFirewall(args []string) int {
	fs := flag.NewFlagSet("spectra network firewall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of raw pf rules")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	result, err := helperclient.New().FirewallRules()
	if err != nil {
		if helperclient.IsUnavailable(err) {
			fmt.Fprintln(os.Stderr, "privileged helper not running; install with: sudo spectra install-helper")
			return 1
		}
		fmt.Fprintf(os.Stderr, "firewall rules: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	if raw, _ := result["raw_rules"].(string); raw != "" {
		fmt.Print(raw)
		if !strings.HasSuffix(raw, "\n") {
			fmt.Println()
		}
		return 0
	}
	fmt.Println("no firewall rules")
	return 0
}

func runNetworkConnections(args []string) int {
	fs := flag.NewFlagSet("spectra network connections", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	filterProto := fs.String("proto", "", "Filter by protocol: tcp or udp")
	filterState := fs.String("state", "", "Filter by connection state (e.g. established)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	conns := netstate.CollectConnections(netstate.DefaultRunner)

	var filtered []netstate.Connection
	for _, c := range conns {
		if *filterProto != "" && c.Proto != strings.ToLower(*filterProto) {
			continue
		}
		if *filterState != "" && c.State != strings.ToLower(*filterState) {
			continue
		}
		filtered = append(filtered, c)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(filtered)
		return 0
	}

	if len(filtered) == 0 {
		fmt.Println("no connections")
		return 0
	}

	fmt.Printf("%-7s  %-5s  %-11s  %-25s  %-25s  %s\n",
		"PID", "PROTO", "STATE", "LOCAL", "REMOTE", "COMMAND")
	fmt.Println(strings.Repeat("-", 90))
	for _, c := range filtered {
		state := c.State
		if state == "" {
			state = "-"
		}
		fmt.Printf("%-7d  %-5s  %-11s  %-25s  %-25s  %s\n",
			c.PID, c.Proto, state,
			truncate(c.LocalAddr, 25),
			truncate(c.RemoteAddr, 25),
			truncate(c.Command, 30))
	}
	return 0
}

func printNetworkState(s netstate.State) {
	fmt.Println("=== Network state ===")
	printNetworkOverview(s)
	printProxyState(s.Proxy)
	printHostsOverrides(s.HostsOverrides)
	printListeningPorts(s.ListeningPorts)
	printProcessThroughput(s.ProcessThroughput)
}

func printNetworkOverview(s netstate.State) {
	if s.DefaultRouteIface != "" {
		fmt.Printf("default route: %s via %s\n", s.DefaultRouteIface, s.DefaultRouteGW)
	}
	if s.EstablishedConnectionsCount > 0 {
		fmt.Printf("connections:   %d established\n", s.EstablishedConnectionsCount)
	}
	if s.VPNActive {
		fmt.Printf("vpn:           active (%s)\n", strings.Join(s.VPNInterfaces, ", "))
	} else {
		fmt.Printf("vpn:           inactive\n")
	}
	if len(s.DNSServers) > 0 {
		fmt.Printf("dns:           %s\n", strings.Join(s.DNSServers, ", "))
	}
	if len(s.ProcessThroughput) > 0 {
		fmt.Printf("throughput:    %d active processes\n", len(s.ProcessThroughput))
	}
}

func printProxyState(proxy netstate.ProxyConfig) {
	if proxy.HTTP == "" && proxy.HTTPS == "" && proxy.SOCKS == "" {
		return
	}
	fmt.Println()
	fmt.Println("proxy:")
	if proxy.HTTP != "" {
		fmt.Printf("  http:  %s\n", proxy.HTTP)
	}
	if proxy.HTTPS != "" {
		fmt.Printf("  https: %s\n", proxy.HTTPS)
	}
	if proxy.SOCKS != "" {
		fmt.Printf("  socks: %s\n", proxy.SOCKS)
	}
}

func printHostsOverrides(hosts []netstate.HostsEntry) {
	if len(hosts) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("hosts overrides (%d):\n", len(hosts))
	for _, h := range hosts {
		fmt.Printf("  %-16s  %s\n", h.IP, strings.Join(h.Names, " "))
	}
}

func printListeningPorts(listening []netstate.ListeningPort) {
	if len(listening) == 0 {
		return
	}

	// Sort by port for stable output.
	ports := make([]netstate.ListeningPort, len(listening))
	copy(ports, listening)
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })

	fmt.Println()
	fmt.Printf("listening ports (%d):\n", len(ports))
	for _, p := range ports {
		fmt.Printf("  %s/%d%s%s\n", p.Proto, p.Port, listeningPID(p), listeningApp(p))
	}
}

func printProcessThroughput(rows []netstate.Throughput) {
	if len(rows) == 0 {
		return
	}
	sorted := append([]netstate.Throughput(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		left := sorted[i].BytesInPerSec + sorted[i].BytesOutPerSec
		right := sorted[j].BytesInPerSec + sorted[j].BytesOutPerSec
		return left > right
	})
	if len(sorted) > 10 {
		sorted = sorted[:10]
	}

	fmt.Println()
	fmt.Printf("network throughput (%d active):\n", len(rows))
	for _, row := range sorted {
		fmt.Printf("  pid=%d %-24s in=%d B/s out=%d B/s\n",
			row.PID, truncate(row.Command, 24), row.BytesInPerSec, row.BytesOutPerSec)
	}
}

func listeningPID(p netstate.ListeningPort) string {
	if p.PID <= 0 {
		return ""
	}
	return fmt.Sprintf(" pid=%d", p.PID)
}

func listeningApp(p netstate.ListeningPort) string {
	if p.AppPath == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", p.AppPath)
}
