package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kaeawc/spectra/internal/netstate"
)

func runNetwork(args []string) int {
	if len(args) > 0 && args[0] == "connections" {
		return runNetworkConnections(args[1:])
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

	if s.DefaultRouteIface != "" {
		fmt.Printf("default route: %s via %s\n", s.DefaultRouteIface, s.DefaultRouteGW)
	}

	if s.VPNActive {
		fmt.Printf("vpn:           active (%s)\n", strings.Join(s.VPNInterfaces, ", "))
	} else {
		fmt.Printf("vpn:           inactive\n")
	}

	if len(s.DNSServers) > 0 {
		fmt.Printf("dns:           %s\n", strings.Join(s.DNSServers, ", "))
	}

	if s.Proxy.HTTP != "" || s.Proxy.HTTPS != "" || s.Proxy.SOCKS != "" {
		fmt.Println()
		fmt.Println("proxy:")
		if s.Proxy.HTTP != "" {
			fmt.Printf("  http:  %s\n", s.Proxy.HTTP)
		}
		if s.Proxy.HTTPS != "" {
			fmt.Printf("  https: %s\n", s.Proxy.HTTPS)
		}
		if s.Proxy.SOCKS != "" {
			fmt.Printf("  socks: %s\n", s.Proxy.SOCKS)
		}
	}

	if len(s.HostsOverrides) > 0 {
		fmt.Println()
		fmt.Printf("hosts overrides (%d):\n", len(s.HostsOverrides))
		for _, h := range s.HostsOverrides {
			fmt.Printf("  %-16s  %s\n", h.IP, strings.Join(h.Names, " "))
		}
	}

	if len(s.ListeningPorts) > 0 {
		// Sort by port for stable output.
		ports := make([]netstate.ListeningPort, len(s.ListeningPorts))
		copy(ports, s.ListeningPorts)
		sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })

		fmt.Println()
		fmt.Printf("listening ports (%d):\n", len(ports))
		for _, p := range ports {
			pid := ""
			if p.PID > 0 {
				pid = fmt.Sprintf(" pid=%d", p.PID)
			}
			app := ""
			if p.AppPath != "" {
				app = fmt.Sprintf(" (%s)", p.AppPath)
			}
			fmt.Printf("  %s/%d%s%s\n", p.Proto, p.Port, pid, app)
		}
	}
}
