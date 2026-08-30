// Package netdiag diagnoses network behavior for a running application by
// joining current process/socket state with bounded endpoint probes.
package netdiag

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// expirySoonDays flags a leaf certificate approaching expiry.
const expirySoonDays = 21

// interceptionVendors are substrings of issuer/subject names used by common
// enterprise TLS-interception proxies. Matching is case-insensitive.
var interceptionVendors = []string{"zscaler", "netskope", "forcepoint", "palo alto", "fortinet", "fortigate", "bluecoat", "blue coat", "mcafee web gateway", "cisco umbrella"}

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

	// Chain is the presented certificate list, leaf first.
	Chain []CertInfo `json:"chain,omitempty"`
	// LeafSPKIPin is the base64 SHA-256 of the leaf SubjectPublicKeyInfo,
	// comparable against an application's certificate-pinning set.
	LeafSPKIPin  string `json:"leaf_spki_pin,omitempty"`
	SigAlgorithm string `json:"signature_algorithm,omitempty"`
	KeyType      string `json:"key_type,omitempty"`
	KeyBits      int    `json:"key_bits,omitempty"`
	// TrustValid reports whether the presented chain validates against the
	// system trust store for ServerName. TrustError explains a failure.
	TrustValid bool   `json:"trust_valid"`
	TrustError string `json:"trust_error,omitempty"`
	// Intercepted flags a likely TLS-interception proxy; InterceptionReason
	// names the signal (known vendor, self-signed leaf, untrusted root).
	Intercepted        bool   `json:"intercepted,omitempty"`
	InterceptionReason string `json:"interception_reason,omitempty"`
	// LeafExpiresInDays is days until the leaf certificate NotAfter;
	// ExpiringSoon is set when that is within expirySoonDays.
	LeafExpiresInDays int  `json:"leaf_expires_in_days,omitempty"`
	ExpiringSoon      bool `json:"expiring_soon,omitempty"`
}

// CertInfo summarizes one certificate in a presented TLS chain.
type CertInfo struct {
	Subject      string `json:"subject"`
	Issuer       string `json:"issuer"`
	NotBefore    string `json:"not_before"`
	NotAfter     string `json:"not_after"`
	IsCA         bool   `json:"is_ca,omitempty"`
	DaysToExpiry int    `json:"days_to_expiry"`
	SPKIPin      string `json:"spki_pin,omitempty"`
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

	// Collect the socket list once and reuse it for both the State's
	// established-connection count and the report's connection list, instead
	// of running `lsof -i -P -n` twice.
	conns := netstate.CollectConnections(run)
	state := netstate.CollectWithConnections(run, conns)
	procs := procRun(ctx, process.CollectOptions{CmdRunner: run})
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
	diagnosticNoEndpoints := len(report.Connections) > 0 &&
		len(targets) == 0 &&
		(len(opts.Targets) > 0 || len(opts.Ports) > 0)
	// Each target's probes (DNS, a up-to-12s traceroute, per-port TCP/TLS
	// dials) are independent and network-I/O-bound, so run them concurrently.
	// Results are written by index to keep output ordering deterministic.
	report.Endpoints = diagnoseEndpoints(ctx, run, dialer, tlsProbe, targets, opts.Timeout)
	report.Findings = findings(report)
	if diagnosticNoEndpoints {
		report.Findings = append(report.Findings, Finding{
			Severity: "warning",
			Title:    "app endpoint filters matched no active connections",
			Detail:   "use broader --ports or positional hosts to include additional remote endpoints",
		})
	}
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

// maxEndpointConcurrency bounds how many endpoints are probed at once. Probes
// are network-I/O-bound (and traceroute can block for ~12s), so a small fixed
// fan-out keeps a many-endpoint diagnosis from serializing into minutes without
// spawning an unbounded number of traceroutes.
const maxEndpointConcurrency = 8

// diagnoseEndpoints probes every target concurrently (bounded) and returns the
// diagnoses in the same order as targets.
func diagnoseEndpoints(ctx context.Context, run netstate.CmdRunner, dialer Dialer, tlsProbe TLSProber, targets []target, timeout time.Duration) []EndpointDiagnosis {
	if len(targets) == 0 {
		return nil
	}
	out := make([]EndpointDiagnosis, len(targets))
	limit := maxEndpointConcurrency
	if len(targets) < limit {
		limit = len(targets)
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = diagnoseEndpoint(ctx, run, dialer, tlsProbe, targets[i], timeout)
		}(i)
	}
	wg.Wait()
	return out
}

// diagnoseEndpoint runs the DNS, traceroute, and per-port TCP/TLS probes for a
// single target. It has no shared mutable state, so callers may run it
// concurrently across targets.
func diagnoseEndpoint(ctx context.Context, run netstate.CmdRunner, dialer Dialer, tlsProbe TLSProber, t target, timeout time.Duration) EndpointDiagnosis {
	diag := EndpointDiagnosis{
		Host:       t.host,
		DNS:        probeDNS(run, t.host),
		Traceroute: probeTrace(run, t.host),
	}
	for _, port := range t.ports {
		pd := PortDiagnosis{Port: port, TCP: probeTCP(ctx, dialer, t.host, port)}
		if port == 443 || port == 8443 {
			tp, err := tlsProbe.ProbeTLS(ctx, t.host, port, timeout)
			if err != nil {
				tp = TLSProbe{Error: err.Error()}
			}
			pd.TLS = &tp
		}
		diag.Ports = append(diag.Ports, pd)
	}
	return diag
}

func probeDNS(run netstate.CmdRunner, host string) DNSProbe {
	start := time.Now()
	out, err := run("dig", "+time=2", "+tries=1", host)
	probe := DNSProbe{DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe = parseDig(out)
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

func parseDig(out []byte) DNSProbe {
	var probe DNSProbe
	for _, rawLine := range bytes.Split(out, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		switch {
		case bytes.Contains(line, []byte("status:")):
			probe.Status = parseDigStatus(string(line))
		case bytes.HasPrefix(line, []byte(";; Query time:")):
			probe.QueryMS = parseDigQueryMS(string(line))
		case bytes.HasPrefix(line, []byte(";; SERVER:")):
			probe.Server = strings.TrimSpace(strings.TrimPrefix(string(line), ";; SERVER:"))
		default:
			if ip := firstIPField(string(line)); ip != "" {
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
	hops := parseTracerouteBytes(out)
	return TraceProbe{OK: len(hops) > 0, Hops: hops}
}

func parseTraceroute(out string) []TraceHop {
	return parseTracerouteBytes([]byte(out))
}

func parseTracerouteBytes(out []byte) []TraceHop {
	var hops []TraceHop
	for _, line := range bytes.Split(out, []byte("\n")) {
		lineStr := string(line)
		fields := strings.Fields(lineStr)
		if len(fields) < 2 {
			continue
		}
		ttl, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		hop := TraceHop{TTL: ttl}
		if strings.Contains(lineStr, "*") {
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
	rawConn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	probe := TLSProbe{ServerName: host, DurationMS: time.Since(start).Milliseconds()}
	if err != nil {
		probe.Error = err.Error()
		return probe, nil
	}
	// Verification stays enabled (no InsecureSkipVerify). When it fails because
	// the chain is untrusted, expired, or intercepted, Go returns a
	// CertificateVerificationError carrying the presented certificates, so the
	// diagnostic still captures and explains the chain rather than hiding it.
	conn := tls.Client(rawConn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	var certs []*x509.Certificate
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		var cve *tls.CertificateVerificationError
		if !errors.As(err, &cve) || len(cve.UnverifiedCertificates) == 0 {
			probe.Error = err.Error()
			return probe, nil
		}
		// Reached the certificate stage: the endpoint speaks TLS but the chain
		// did not validate. Report it via the trust/interception fields.
		certs = cve.UnverifiedCertificates
		probe.OK = true
	} else {
		defer conn.Close()
		state := conn.ConnectionState()
		probe.OK = true
		probe.Version = tlsVersion(state.Version)
		probe.ALPN = state.NegotiatedProtocol
		certs = state.PeerCertificates
	}
	if len(certs) > 0 {
		cert := certs[0]
		probe.Subject = cert.Subject.String()
		probe.Issuer = cert.Issuer.String()
		probe.DNSNames = cert.DNSNames
		probe.ZscalerHint = strings.Contains(strings.ToLower(probe.Issuer), "zscaler") || strings.Contains(strings.ToLower(probe.Subject), "zscaler")
	}
	analyzeChain(&probe, host, certs, nil, time.Now())
	return probe, nil
}

// analyzeChain populates the chain, key, trust, interception, and expiry fields
// on probe from the presented certificates. roots nil uses the system trust
// store; now is injected so results are deterministic under test.
func analyzeChain(probe *TLSProbe, host string, certs []*x509.Certificate, roots *x509.CertPool, now time.Time) {
	if len(certs) == 0 {
		return
	}
	leaf := certs[0]
	probe.LeafSPKIPin = spkiPin(leaf)
	probe.SigAlgorithm = leaf.SignatureAlgorithm.String()
	probe.KeyType, probe.KeyBits = keyDetail(leaf)
	for _, c := range certs {
		probe.Chain = append(probe.Chain, CertInfo{
			Subject:      certName(c.Subject),
			Issuer:       certName(c.Issuer),
			NotBefore:    c.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:     c.NotAfter.UTC().Format(time.RFC3339),
			IsCA:         c.IsCA,
			DaysToExpiry: daysUntil(c.NotAfter, now),
			SPKIPin:      spkiPin(c),
		})
	}

	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: roots, CurrentTime: now}); err == nil {
		probe.TrustValid = true
	} else {
		probe.TrustError = err.Error()
	}

	probe.Intercepted, probe.InterceptionReason = interceptionSignal(leaf, probe.TrustValid, probe.TrustError)
	probe.LeafExpiresInDays = daysUntil(leaf.NotAfter, now)
	probe.ExpiringSoon = probe.LeafExpiresInDays <= expirySoonDays
}

// interceptionSignal reports whether the leaf certificate looks like it comes
// from a TLS-interception proxy, and the signal that matched.
//
// Limitation: an interception root installed into the system trust store under
// an unrecognized name makes the chain validate as trusted, so it is only
// caught here by the vendor-name check — pure Go exposes no per-anchor trust
// provenance to distinguish a private/enterprise root from a public CA.
func interceptionSignal(leaf *x509.Certificate, trustValid bool, trustErr string) (bool, string) {
	hay := strings.ToLower(leaf.Issuer.String() + " " + leaf.Subject.String())
	for _, v := range interceptionVendors {
		if strings.Contains(hay, v) {
			return true, "issuer matches known interception vendor: " + v
		}
	}
	if leaf.Issuer.String() == leaf.Subject.String() && !leaf.IsCA && isSelfSigned(leaf) {
		return true, "leaf certificate is self-signed"
	}
	if !trustValid && strings.Contains(strings.ToLower(trustErr), "unknown authority") {
		return true, "chain does not validate to a trusted root"
	}
	return false, ""
}

// isSelfSigned confirms the certificate signature verifies against its own
// public key, so a matching issuer/subject name issued by a different key is
// not mistaken for self-signed.
func isSelfSigned(cert *x509.Certificate) bool {
	return cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
}

func spkiPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func keyDetail(cert *x509.Certificate) (string, int) {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return "RSA", pub.N.BitLen()
	case *ecdsa.PublicKey:
		return "ECDSA", pub.Curve.Params().BitSize
	case ed25519.PublicKey:
		return "Ed25519", len(pub) * 8
	default:
		return "", 0
	}
}

// certName prefers the common name, falling back to the full RDN string.
func certName(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	return n.String()
}

func daysUntil(t, now time.Time) int {
	return int(t.Sub(now).Hours() / 24)
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
