// Package netdiag diagnoses network behavior for a running application by
// joining current process/socket state with bounded endpoint probes.
package netdiag

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
)

const defaultTimeout = 3 * time.Second

const (
	slowDNSMS = 500
	slowTCPMS = 750
	slowTLSMS = 1000
)

// Options controls one app-focused network diagnosis.
type Options struct {
	AppPath string
	PID     int
	Command string
	Targets []string
	Ports   []int
	Timeout time.Duration

	NetRunner  netstate.CmdRunner
	ProcRunner func(context.Context, process.CollectOptions) []process.Info
	Dialer     Dialer
	TLSProbe   TLSProber
}

// Dialer is the subset of net.Dialer used by diagnosis probes.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// TLSProber captures TLS handshake metadata for a host:port.
type TLSProber interface {
	ProbeTLS(ctx context.Context, host string, port int, timeout time.Duration) (TLSProbe, error)
}

// Report is the app-centric diagnosis result.
type Report struct {
	AppPath       string                `json:"app_path,omitempty"`
	PID           int                   `json:"pid,omitempty"`
	Command       string                `json:"command,omitempty"`
	Network       netstate.State        `json:"network"`
	Processes     []ProcessSummary      `json:"processes,omitempty"`
	Connections   []netstate.Connection `json:"connections,omitempty"`
	Throughput    []netstate.Throughput `json:"throughput,omitempty"`
	TopThroughput []netstate.Throughput `json:"top_throughput,omitempty"`
	Endpoints     []EndpointDiagnosis   `json:"endpoints,omitempty"`
	Findings      []Finding             `json:"findings,omitempty"`
}

// ProcessSummary is the process identity included in reports.
type ProcessSummary struct {
	PID            int    `json:"pid"`
	Command        string `json:"command"`
	ExecutablePath string `json:"executable_path,omitempty"`
	AppPath        string `json:"app_path,omitempty"`
}

// EndpointDiagnosis is the probe result for one remote host.
type EndpointDiagnosis struct {
	Host       string          `json:"host"`
	Ports      []PortDiagnosis `json:"ports,omitempty"`
	DNS        DNSProbe        `json:"dns"`
	Traceroute TraceProbe      `json:"traceroute"`
}

// PortDiagnosis contains connect/TLS timing for one host:port.
type PortDiagnosis struct {
	Port int       `json:"port"`
	TCP  TCPProbe  `json:"tcp"`
	TLS  *TLSProbe `json:"tls,omitempty"`
}

type DNSProbe struct {
	OK         bool     `json:"ok"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	Status     string   `json:"status,omitempty"`
	Server     string   `json:"server,omitempty"`
	QueryMS    int64    `json:"query_ms,omitempty"`
	Addresses  []string `json:"addresses,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type TCPProbe struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

type TLSProbe struct {
	OK          bool     `json:"ok"`
	DurationMS  int64    `json:"duration_ms,omitempty"`
	ServerName  string   `json:"server_name,omitempty"`
	Version     string   `json:"version,omitempty"`
	Issuer      string   `json:"issuer,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	ALPN        string   `json:"alpn,omitempty"`
	DNSNames    []string `json:"dns_names,omitempty"`
	ZscalerHint bool     `json:"zscaler_hint,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type TraceProbe struct {
	OK    bool       `json:"ok"`
	Hops  []TraceHop `json:"hops,omitempty"`
	Error string     `json:"error,omitempty"`
}

type TraceHop struct {
	TTL     int      `json:"ttl"`
	Hosts   []string `json:"hosts,omitempty"`
	Latency string   `json:"latency,omitempty"`
	Timeout bool     `json:"timeout,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
}

// Diagnose collects local app network state and runs bounded probes against
// explicitly supplied targets plus currently connected remote endpoints.
func Diagnose(ctx context.Context, opts Options) (Report, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	run := opts.NetRunner
	if run == nil {
		run = netstate.DefaultRunner
	}
	procRun := opts.ProcRunner
	if procRun == nil {
		procRun = process.CollectAll
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: opts.Timeout}
	}
	tlsProbe := opts.TLSProbe
	if tlsProbe == nil {
		tlsProbe = realTLSProber{dialer: &net.Dialer{Timeout: opts.Timeout}}
	}

	state := netstate.Collect(run)
	procs := procRun(ctx, process.CollectOptions{CmdRunner: run})
	conns := netstate.CollectConnections(run)
	report := Report{
		AppPath: opts.AppPath,
		PID:     opts.PID,
		Command: opts.Command,
		Network: state,
	}
	report.Processes = filterProcesses(procs, opts)
	report.Connections = filterConnections(conns, report.Processes, opts)
	report.Throughput = filterThroughput(state.ProcessThroughput, report.Processes, opts)
	report.TopThroughput = topThroughput(state.ProcessThroughput, 5)

	targets := endpointTargets(opts.Targets, report.Connections, opts.Ports)
	for _, target := range targets {
		diag := EndpointDiagnosis{
			Host:       target.host,
			DNS:        probeDNS(run, target.host),
			Traceroute: probeTrace(run, target.host),
		}
		for _, port := range target.ports {
			pd := PortDiagnosis{Port: port, TCP: probeTCP(ctx, dialer, target.host, port)}
			if port == 443 || port == 8443 {
				tp, err := tlsProbe.ProbeTLS(ctx, target.host, port, opts.Timeout)
				if err != nil {
					tp = TLSProbe{Error: err.Error()}
				}
				pd.TLS = &tp
			}
			diag.Ports = append(diag.Ports, pd)
		}
		report.Endpoints = append(report.Endpoints, diag)
	}
	report.Findings = findings(report)
	return report, nil
}

func filterProcesses(procs []process.Info, opts Options) []ProcessSummary {
	var out []ProcessSummary
	for _, p := range procs {
		if opts.PID > 0 && p.PID != opts.PID {
			continue
		}
		if opts.AppPath != "" && p.AppPath != opts.AppPath && !strings.HasPrefix(p.ExecutablePath, opts.AppPath+"/") {
			continue
		}
		if opts.Command != "" && !strings.EqualFold(p.Command, opts.Command) && !strings.Contains(strings.ToLower(p.FullCommandLine), strings.ToLower(opts.Command)) {
			continue
		}
		out = append(out, ProcessSummary{PID: p.PID, Command: p.Command, ExecutablePath: p.ExecutablePath, AppPath: p.AppPath})
	}
	return out
}

func filterConnections(conns []netstate.Connection, procs []ProcessSummary, opts Options) []netstate.Connection {
	pids := processPIDSet(procs)
	var out []netstate.Connection
	for _, c := range conns {
		if opts.PID > 0 && c.PID != opts.PID {
			continue
		}
		if len(pids) > 0 && !pids[c.PID] {
			continue
		}
		if opts.Command != "" && !strings.EqualFold(c.Command, opts.Command) && len(pids) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

func filterThroughput(rows []netstate.Throughput, procs []ProcessSummary, opts Options) []netstate.Throughput {
	pids := processPIDSet(procs)
	var out []netstate.Throughput
	for _, row := range rows {
		if opts.PID > 0 && row.PID != opts.PID {
			continue
		}
		if len(pids) > 0 && !pids[row.PID] {
			continue
		}
		if opts.Command != "" && !strings.EqualFold(row.Command, opts.Command) && len(pids) == 0 {
			continue
		}
		out = append(out, row)
	}
	return out
}

func topThroughput(rows []netstate.Throughput, limit int) []netstate.Throughput {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	out := append([]netstate.Throughput(nil), rows...)
	sort.Slice(out, func(i, j int) bool {
		return throughputTotal(out[i]) > throughputTotal(out[j])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func throughputTotal(row netstate.Throughput) int64 {
	return row.BytesInPerSec + row.BytesOutPerSec
}

func processPIDSet(procs []ProcessSummary) map[int]bool {
	out := make(map[int]bool, len(procs))
	for _, p := range procs {
		out[p.PID] = true
	}
	return out
}

type target struct {
	host  string
	ports []int
}

func endpointTargets(explicit []string, conns []netstate.Connection, portFilters []int) []target {
	if len(conns) > 0 {
		return connectionTargets(explicit, conns, portFilters)
	}
	seen := map[string]map[int]bool{}
	for _, raw := range explicit {
		host, ports := splitTarget(raw, portFilters)
		addTarget(seen, host, ports)
	}
	return targetsFromSeen(seen)
}

func connectionTargets(filters []string, conns []netstate.Connection, portFilters []int) []target {
	hostFilters := map[string]bool{}
	seen := map[string]map[int]bool{}
	for _, raw := range filters {
		host, port := splitFilterTarget(raw)
		if host != "" {
			hostFilters[strings.ToLower(host)] = true
		}
		if port > 0 {
			portFilters = append(portFilters, port)
		}
	}
	for _, c := range conns {
		if c.RemoteAddr == "" {
			continue
		}
		host, port := splitHostPortLoose(c.RemoteAddr)
		if host == "" {
			continue
		}
		if len(hostFilters) > 0 && !hostFilters[strings.ToLower(host)] {
			continue
		}
		if len(portFilters) > 0 && !containsPort(portFilters, port) {
			continue
		}
		addTarget(seen, host, []int{port})
	}
	return targetsFromSeen(seen)
}

func splitFilterTarget(raw string) (string, int) {
	return splitHostPortLoose(raw)
}

func targetsFromSeen(seen map[string]map[int]bool) []target {
	var out []target
	for host, ports := range seen {
		var ps []int
		for port := range ports {
			ps = append(ps, port)
		}
		sort.Ints(ps)
		out = append(out, target{host: host, ports: ps})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].host < out[j].host })
	return out
}

func splitTarget(raw string, portFilters []int) (string, []int) {
	host, port := splitHostPortLoose(raw)
	if port > 0 {
		return host, []int{port}
	}
	if len(portFilters) > 0 {
		return host, portFilters
	}
	return host, []int{443}
}

func addTarget(seen map[string]map[int]bool, host string, ports []int) {
	host = strings.Trim(host, "[] ")
	if host == "" || host == "*" {
		return
	}
	if seen[host] == nil {
		seen[host] = map[int]bool{}
	}
	for _, port := range ports {
		if port > 0 {
			seen[host][port] = true
		}
	}
}

func containsPort(ports []int, want int) bool {
	for _, port := range ports {
		if port == want {
			return true
		}
	}
	return false
}

func splitHostPortLoose(addr string) (string, int) {
	host, portRaw, err := net.SplitHostPort(addr)
	if err == nil {
		port, _ := strconv.Atoi(portRaw)
		return host, port
	}
	idx := strings.LastIndex(addr, ":")
	if idx <= 0 {
		return strings.Trim(addr, "[]"), 0
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil {
		return strings.Trim(addr, "[]"), 0
	}
	return strings.Trim(addr[:idx], "[]"), port
}

func probeDNS(run netstate.CmdRunner, host string) DNSProbe {
	start := time.Now()
	out, err := run("dig", "+time=2", "+tries=1", host)
	probe := DNSProbe{DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe = parseDig(string(out))
	if probe.DurationMS == 0 {
		probe.DurationMS = time.Since(start).Milliseconds()
	}
	if probe.Status == "" {
		probe.Status = "unknown"
	}
	probe.OK = strings.EqualFold(probe.Status, "NOERROR") && len(probe.Addresses) > 0
	if !probe.OK && probe.Error == "" {
		probe.Error = "dns status " + probe.Status
	}
	return probe
}

func parseDig(out string) DNSProbe {
	var probe DNSProbe
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "status:"):
			probe.Status = parseDigStatus(line)
		case strings.HasPrefix(line, ";; Query time:"):
			probe.QueryMS = parseDigQueryMS(line)
		case strings.HasPrefix(line, ";; SERVER:"):
			probe.Server = strings.TrimSpace(strings.TrimPrefix(line, ";; SERVER:"))
		default:
			if ip := firstIPField(line); ip != "" {
				probe.Addresses = append(probe.Addresses, ip)
			}
		}
	}
	return probe
}

func parseDigStatus(line string) string {
	_, after, ok := strings.Cut(line, "status:")
	if !ok {
		return ""
	}
	status := strings.TrimSpace(after)
	if idx := strings.Index(status, ","); idx >= 0 {
		status = status[:idx]
	}
	return strings.TrimSpace(status)
}

func parseDigQueryMS(line string) int64 {
	fields := strings.Fields(line)
	for i, field := range fields {
		if field == "time:" && i+1 < len(fields) {
			ms, _ := strconv.ParseInt(fields[i+1], 10, 64)
			return ms
		}
	}
	return 0
}

func firstIPField(line string) string {
	for _, field := range strings.Fields(line) {
		if net.ParseIP(field) != nil {
			return field
		}
	}
	return ""
}

func probeTCP(ctx context.Context, dialer Dialer, host string, port int) TCPProbe {
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	probe := TCPProbe{DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	_ = conn.Close()
	probe.OK = true
	return probe
}

func probeTrace(run netstate.CmdRunner, host string) TraceProbe {
	out, err := run("traceroute", "-n", "-m", "12", "-w", "1", host)
	if err != nil {
		return TraceProbe{Error: err.Error()}
	}
	hops := parseTraceroute(string(out))
	return TraceProbe{OK: len(hops) > 0, Hops: hops}
}

func parseTraceroute(out string) []TraceHop {
	var hops []TraceHop
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ttl, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		hop := TraceHop{TTL: ttl}
		if strings.Contains(line, "*") {
			hop.Timeout = true
		}
		for _, f := range fields[1:] {
			if net.ParseIP(f) != nil {
				hop.Hosts = append(hop.Hosts, f)
				continue
			}
			if strings.HasSuffix(f, "ms") {
				hop.Latency = f
			}
		}
		hops = append(hops, hop)
	}
	return hops
}

type realTLSProber struct {
	dialer *net.Dialer
}

func (p realTLSProber) ProbeTLS(ctx context.Context, host string, port int, timeout time.Duration) (TLSProbe, error) {
	start := time.Now()
	d := p.dialer
	if d == nil {
		d = &net.Dialer{Timeout: timeout}
	}
	conn, err := tls.DialWithDialer(d, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	probe := TLSProbe{ServerName: host, DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe, nil
	}
	defer conn.Close()
	state := conn.ConnectionState()
	probe.OK = true
	probe.Version = tlsVersion(state.Version)
	probe.ALPN = state.NegotiatedProtocol
	if len(state.PeerCertificates) > 0 {
		cert := state.PeerCertificates[0]
		probe.Subject = cert.Subject.String()
		probe.Issuer = cert.Issuer.String()
		probe.DNSNames = cert.DNSNames
		probe.ZscalerHint = strings.Contains(strings.ToLower(probe.Issuer), "zscaler") || strings.Contains(strings.ToLower(probe.Subject), "zscaler")
	}
	return probe, nil
}

func tlsVersion(v uint16) string {
	switch v {
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func findings(report Report) []Finding {
	var out []Finding
	if len(report.Processes) == 0 && (report.AppPath != "" || report.PID > 0 || report.Command != "") {
		out = append(out, Finding{Severity: "warning", Title: "no matching running process", Detail: "diagnosis may only reflect explicit targets and host network state"})
	}
	if len(report.Connections) == 0 && len(report.Processes) > 0 {
		out = append(out, Finding{Severity: "info", Title: "matched app has no active network sockets"})
	}
	if f, ok := bandwidthFinding(report); ok {
		out = append(out, f)
	}
	if report.Network.Proxy.HTTP != "" || report.Network.Proxy.HTTPS != "" || report.Network.Proxy.SOCKS != "" {
		out = append(out, Finding{Severity: "info", Title: "system proxy configured", Detail: proxyDetail(report.Network.Proxy)})
		if proxyZscalerHint(report.Network.Proxy) {
			out = append(out, Finding{Severity: "info", Title: "zscaler-like proxy configured", Detail: proxyDetail(report.Network.Proxy)})
		}
	}
	if report.Network.VPNActive {
		out = append(out, Finding{Severity: "info", Title: "vpn/tunnel interfaces active", Detail: strings.Join(report.Network.VPNInterfaces, ", ")})
	}
	for _, ep := range report.Endpoints {
		out = append(out, endpointFindings(ep)...)
	}
	return out
}

func endpointFindings(ep EndpointDiagnosis) []Finding {
	var out []Finding
	if !ep.DNS.OK {
		out = append(out, Finding{Severity: "warning", Title: "dns lookup failed", Detail: ep.Host + ": " + ep.DNS.Error})
	}
	if ep.DNS.QueryMS >= slowDNSMS {
		out = append(out, Finding{Severity: "warning", Title: "slow dns lookup", Detail: fmt.Sprintf("%s query_ms=%d", ep.Host, ep.DNS.QueryMS)})
	}
	if len(ep.Traceroute.Hops) > 0 && ep.Traceroute.Hops[len(ep.Traceroute.Hops)-1].Timeout {
		out = append(out, Finding{Severity: "info", Title: "traceroute has unanswered hops", Detail: ep.Host})
	}
	for _, port := range ep.Ports {
		out = append(out, portFindings(ep.Host, port)...)
	}
	return out
}

func portFindings(host string, port PortDiagnosis) []Finding {
	var out []Finding
	if !port.TCP.OK {
		out = append(out, Finding{Severity: "warning", Title: "tcp connect failed", Detail: fmt.Sprintf("%s:%d %s", host, port.Port, port.TCP.Error)})
	}
	if port.TCP.OK && port.TCP.DurationMS >= slowTCPMS {
		out = append(out, Finding{Severity: "warning", Title: "slow tcp connect", Detail: fmt.Sprintf("%s:%d connect_ms=%d", host, port.Port, port.TCP.DurationMS)})
	}
	if port.TLS != nil && port.TLS.OK && port.TLS.DurationMS >= slowTLSMS {
		out = append(out, Finding{Severity: "warning", Title: "slow tls handshake", Detail: fmt.Sprintf("%s:%d tls_ms=%d", host, port.Port, port.TLS.DurationMS)})
	}
	if port.TLS != nil && port.TLS.ZscalerHint {
		out = append(out, Finding{Severity: "info", Title: "zscaler tls issuer/subject observed", Detail: host})
	}
	return out
}

func proxyZscalerHint(proxy netstate.ProxyConfig) bool {
	raw := strings.ToLower(proxy.HTTP + " " + proxy.HTTPS + " " + proxy.SOCKS)
	return strings.Contains(raw, "zscaler") || strings.Contains(raw, "zscloud")
}

func bandwidthFinding(report Report) (Finding, bool) {
	appPIDs := processPIDSet(report.Processes)
	var appBytes int64
	for _, row := range report.Throughput {
		appBytes += throughputTotal(row)
	}
	for _, row := range report.TopThroughput {
		if appPIDs[row.PID] {
			continue
		}
		total := throughputTotal(row)
		if total > 0 && (appBytes == 0 || total >= appBytes*4) {
			return Finding{
				Severity: "info",
				Title:    "other process dominates current network throughput",
				Detail:   fmt.Sprintf("pid=%d %s total=%d B/s diagnosed_app_total=%d B/s", row.PID, row.Command, total, appBytes),
			}, true
		}
	}
	return Finding{}, false
}

func proxyDetail(proxy netstate.ProxyConfig) string {
	var parts []string
	if proxy.HTTP != "" {
		parts = append(parts, "http="+proxy.HTTP)
	}
	if proxy.HTTPS != "" {
		parts = append(parts, "https="+proxy.HTTPS)
	}
	if proxy.SOCKS != "" {
		parts = append(parts, "socks="+proxy.SOCKS)
	}
	return strings.Join(parts, " ")
}
