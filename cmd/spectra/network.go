package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/netcap"
	"github.com/kaeawc/spectra/internal/netstate"
)

var (
	networkCaptureStart = func(iface string, durationMS, snapLen int, proto, host string, port int) (map[string]any, error) {
		return helperclient.New().NetCaptureStart(iface, durationMS, snapLen, proto, host, port)
	}
	networkCaptureStop = func(handle string) (map[string]any, error) {
		return helperclient.New().NetCaptureStop(handle)
	}
	networkCaptureSummarize = func(path string, limit int) (netcap.PCAPSummary, error) {
		f, err := os.Open(path)
		if err != nil {
			return netcap.PCAPSummary{}, fmt.Errorf("open pcap: %w", err)
		}
		defer f.Close()
		return netcap.SummarizePCAP(f, limit)
	}
)

func runNetwork(args []string) int {
	if len(args) > 0 && args[0] == "connections" {
		return runNetworkConnections(args[1:])
	}
	if len(args) > 0 && args[0] == "firewall" {
		return runNetworkFirewall(args[1:])
	}
	if len(args) > 0 && args[0] == "capture" {
		return runNetworkCapture(args[1:])
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

func runNetworkCapture(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: spectra network capture [start|stop|summarize] ...")
		return 2
	}
	switch args[0] {
	case "start":
		return runNetworkCaptureStart(args[1:])
	case "stop":
		return runNetworkCaptureStop(args[1:])
	case "summarize", "summary":
		return runNetworkCaptureSummarize(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown network capture command %q\n", args[0])
		return 2
	}
}

func runNetworkCaptureStart(args []string) int {
	fs := flag.NewFlagSet("spectra network capture start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	iface := fs.String("interface", "", "Network interface to capture, e.g. en0 or utun3")
	duration := fs.Duration("duration", 30*time.Second, "Maximum capture duration")
	snapLen := fs.Int("snaplen", 0, "tcpdump snapshot length (0 = default)")
	proto := fs.String("proto", "", "Optional protocol filter: tcp or udp")
	host := fs.String("host", "", "Optional host filter")
	port := fs.Int("port", 0, "Optional port filter")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *iface == "" {
		fmt.Fprintln(os.Stderr, "network capture start requires --interface")
		return 2
	}
	durationMS := int(duration.Milliseconds())
	result, err := networkCaptureStart(*iface, durationMS, *snapLen, *proto, *host, *port)
	if err != nil {
		if helperclient.IsUnavailable(err) {
			fmt.Fprintln(os.Stderr, "privileged helper not running; install with: sudo spectra install-helper")
			return 1
		}
		fmt.Fprintf(os.Stderr, "network capture start: %v\n", err)
		return 1
	}
	return printNetworkCaptureResult(result, *asJSON, "capture started")
}

func runNetworkCaptureStop(args []string) int {
	fs := flag.NewFlagSet("spectra network capture stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	summarize := fs.Bool("summarize", false, "Summarize the completed pcap after stopping")
	limit := fs.Int("limit", netcap.DefaultSummaryEventLimit, "Maximum protocol events to include when summarizing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra network capture stop [--json] [--summarize] [--limit N] <handle>")
		return 2
	}
	result, err := networkCaptureStop(fs.Arg(0))
	if err != nil {
		if helperclient.IsUnavailable(err) {
			fmt.Fprintln(os.Stderr, "privileged helper not running; install with: sudo spectra install-helper")
			return 1
		}
		fmt.Fprintf(os.Stderr, "network capture stop: %v\n", err)
		return 1
	}
	if *summarize {
		path, _ := result["output_path"].(string)
		if path == "" {
			fmt.Fprintln(os.Stderr, "network capture stop: helper result did not include output_path")
			return 1
		}
		summary, err := networkCaptureSummarize(path, *limit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "network capture summarize: %v\n", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"capture": result, "summary": summary})
			return 0
		}
		printNetworkCaptureResult(result, false, "capture stopped")
		printNetworkCaptureSummary(summary)
		return 0
	}
	return printNetworkCaptureResult(result, *asJSON, "capture stopped")
}

func runNetworkCaptureSummarize(args []string) int {
	fs := flag.NewFlagSet("spectra network capture summarize", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "Emit JSON instead of a human summary")
	limit := fs.Int("limit", netcap.DefaultSummaryEventLimit, "Maximum protocol events to include")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spectra network capture summarize [--json] [--limit N] <pcap-path>")
		return 2
	}
	summary, err := networkCaptureSummarize(fs.Arg(0), *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "network capture summarize: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
		return 0
	}
	printNetworkCaptureSummary(summary)
	return 0
}

func printNetworkCaptureResult(result map[string]any, asJSON bool, label string) int {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}
	fmt.Println(label)
	for _, key := range []string{"handle", "interface", "duration_ms", "output_path", "size_bytes", "wait_error", "owner_error", "stat_error"} {
		if v, ok := result[key]; ok && fmt.Sprint(v) != "" {
			fmt.Printf("  %s: %s\n", key, formatCaptureValue(v))
		}
	}
	return 0
}

func printNetworkCaptureSummary(summary netcap.PCAPSummary) {
	fmt.Println("capture summary")
	fmt.Printf("  packets: %d\n", summary.Packets)
	fmt.Printf("  decoded_flows: %d\n", summary.DecodedFlows)
	if summary.DecodeErrors > 0 {
		fmt.Printf("  decode_errors: %d\n", summary.DecodeErrors)
	}
	fmt.Printf("  dns: %d\n", len(summary.DNS))
	fmt.Printf("  tls_client_hello: %d\n", len(summary.TLS))
	fmt.Printf("  http: %d\n", len(summary.HTTP))
	if upgrades := countWebSocketUpgrades(summary); upgrades > 0 {
		fmt.Printf("  websocket_upgrades: %d\n", upgrades)
	}
	if summary.EventsDropped > 0 {
		fmt.Printf("  events_dropped: %d\n", summary.EventsDropped)
	}
	for _, event := range summary.DNS {
		for _, q := range event.Message.Questions {
			fmt.Printf("  dns_query: %s %s %s -> %s\n", q.Name, q.Type, formatFlowEndpoint(event.Flow), formatFlowDestination(event.Flow))
		}
	}
	for _, event := range summary.TLS {
		hello := event.ClientHello
		target := hello.SNI
		if target == "" && hello.ECHPresent {
			target = "ech-present"
		}
		if target != "" {
			fmt.Printf("  tls_client_hello: %s %s -> %s\n", target, formatFlowEndpoint(event.Flow), formatFlowDestination(event.Flow))
		}
	}
	for _, event := range summary.HTTP {
		msg := event.Message
		if msg.IsRequest {
			label := "http_request"
			if msg.WebSocket {
				label = "websocket_upgrade"
			}
			fmt.Printf("  %s: %s %s %s -> %s\n", label, msg.Method, msg.Target, formatFlowEndpoint(event.Flow), formatFlowDestination(event.Flow))
		} else {
			label := "http_response"
			if msg.WebSocket {
				label = "websocket_upgrade_response"
			}
			fmt.Printf("  %s: %d %s %s -> %s\n", label, msg.StatusCode, msg.Reason, formatFlowEndpoint(event.Flow), formatFlowDestination(event.Flow))
		}
	}
}

func countWebSocketUpgrades(summary netcap.PCAPSummary) int {
	var count int
	for _, event := range summary.HTTP {
		if event.Message.WebSocket {
			count++
		}
	}
	return count
}

func formatFlowEndpoint(flow netcap.FlowSummary) string {
	return fmt.Sprintf("%s:%d", flow.SrcAddr, flow.SrcPort)
}

func formatFlowDestination(flow netcap.FlowSummary) string {
	return fmt.Sprintf("%s:%d", flow.DstAddr, flow.DstPort)
}

func formatCaptureValue(v any) string {
	switch n := v.(type) {
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
	}
	return fmt.Sprint(v)
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
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		if ports[i].LocalAddr != ports[j].LocalAddr {
			return ports[i].LocalAddr < ports[j].LocalAddr
		}
		return ports[i].PID < ports[j].PID
	})

	fmt.Println()
	fmt.Printf("listening ports (%d):\n", len(ports))
	for _, p := range ports {
		fmt.Printf("  %s/%d%s%s%s%s\n", p.Proto, p.Port, listeningAddr(p), listeningPID(p), listeningCommand(p), listeningApp(p))
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

func listeningAddr(p netstate.ListeningPort) string {
	if p.LocalAddr == "" {
		return ""
	}
	return fmt.Sprintf(" addr=%s", p.LocalAddr)
}

func listeningCommand(p netstate.ListeningPort) string {
	if p.Command == "" {
		return ""
	}
	return fmt.Sprintf(" cmd=%s", truncate(p.Command, 24))
}

func listeningApp(p netstate.ListeningPort) string {
	if p.AppPath == "" {
		return ""
	}
	return fmt.Sprintf(" (%s)", p.AppPath)
}
