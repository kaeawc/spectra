package netdiag

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/process"
)

func TestDiagnoseAppNetworkBehavior(t *testing.T) {
	run := appBehaviorRunner(t)
	procRun := func(context.Context, process.CollectOptions) []process.Info {
		return []process.Info{{PID: 412, Command: "Slack", ExecutablePath: "/Applications/Slack.app/Contents/MacOS/Slack", AppPath: "/Applications/Slack.app"}}
	}
	report, err := Diagnose(context.Background(), Options{
		AppPath:    "/Applications/Slack.app",
		NetRunner:  run,
		ProcRunner: procRun,
		Dialer:     fakeDialer{},
		TLSProbe:   fakeTLS{issuer: "CN=Zscaler Root CA"},
	})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(report.Processes) != 1 || len(report.Connections) != 1 || len(report.Throughput) != 1 {
		t.Fatalf("report process state = %+v", report)
	}
	if len(report.TopThroughput) != 2 || report.TopThroughput[0].Command != "backupd" {
		t.Fatalf("top throughput = %+v", report.TopThroughput)
	}
	if len(report.Endpoints) != 1 || report.Endpoints[0].Host != "api.example.com" {
		t.Fatalf("endpoints = %+v", report.Endpoints)
	}
	if !report.Endpoints[0].DNS.OK || report.Endpoints[0].DNS.Status != "NOERROR" || report.Endpoints[0].DNS.QueryMS != 7 || !report.Endpoints[0].Ports[0].TCP.OK {
		t.Fatalf("probes = %+v", report.Endpoints[0])
	}
	if len(report.Findings) < 3 {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func appBehaviorRunner(t *testing.T) func(string, ...string) ([]byte, error) {
	t.Helper()
	fixtures := map[string][]byte{
		"route -n get default": []byte("interface: en0\ngateway: 192.0.2.1\n"),
		"scutil --dns":         []byte("nameserver[0] : 1.1.1.1\n"),
		"scutil --proxy":       []byte("HTTPSEnable : 1\nHTTPSProxy : zscaler.example\nHTTPSPort : 9443\n"),
		"ifconfig":             []byte("utun4: flags=8051<UP,POINTOPOINT,RUNNING>\n\tinet 100.64.0.1\n"),
		"nettop -P -L 2 -x -d -J bytes_in,bytes_out -t external": []byte(",bytes_in,bytes_out,\nSlack.412,100,50,\nbackupd.900,1000,1000,\n"),
		"lsof -i -P -n":                            []byte("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nSlack 412 alice 29u IPv4 0xabc 0t0 TCP 192.0.2.10:55123->api.example.com:443 (ESTABLISHED)\n"),
		"dig +time=2 +tries=1 api.example.com":     []byte(digNOERRORFixture),
		"traceroute -n -m 12 -w 1 api.example.com": []byte("1 192.0.2.1 1.0 ms\n2 203.0.113.10 10.0 ms\n"),
	}
	return func(name string, args ...string) ([]byte, error) {
		cmd := fakeCommand(name, args...)
		if cmd == "lsof -i -P -n -sTCP:LISTEN" {
			return nil, errors.New("none")
		}
		if out, ok := fixtures[cmd]; ok {
			return out, nil
		}
		t.Fatalf("unexpected command: %s", cmd)
		return nil, nil
	}
}

func TestDiagnoseExplicitTargetAndFailures(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		cmd := fakeCommand(name, args...)
		switch {
		case strings.HasPrefix(cmd, "dig "):
			return []byte(""), nil
		case strings.HasPrefix(cmd, "traceroute "):
			return nil, errors.New("traceroute unavailable")
		case cmd == "route -n get default", cmd == "scutil --dns", cmd == "scutil --proxy", cmd == "ifconfig", strings.HasPrefix(cmd, "lsof "), strings.HasPrefix(cmd, "nettop "):
			return []byte(""), nil
		default:
			t.Fatalf("unexpected command: %s", cmd)
		}
		return nil, nil
	}
	report, err := Diagnose(context.Background(), Options{
		Targets:   []string{"blocked.example:443"},
		NetRunner: run,
		ProcRunner: func(context.Context, process.CollectOptions) []process.Info {
			return nil
		},
		Dialer: fakeDialer{err: errors.New("timeout")},
		TLSProbe: fakeTLS{
			err: errors.New("tls timeout"),
		},
	})
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(report.Endpoints) != 1 || report.Endpoints[0].DNS.OK {
		t.Fatalf("endpoint = %+v", report.Endpoints)
	}
	if len(report.Findings) < 2 {
		t.Fatalf("findings = %+v", report.Findings)
	}
}

func TestEndpointTargetsInferAndFilterAppConnections(t *testing.T) {
	conns := []netstate.Connection{
		{RemoteAddr: "api.example.com:443"},
		{RemoteAddr: "uploads.example.com:8443"},
		{RemoteAddr: "api.example.com:5228"},
	}
	all := endpointTargets(nil, conns, nil)
	if len(all) != 2 || all[0].host != "api.example.com" || len(all[0].ports) != 2 {
		t.Fatalf("all targets = %+v", all)
	}
	hostFiltered := endpointTargets([]string{"uploads.example.com"}, conns, nil)
	if len(hostFiltered) != 1 || hostFiltered[0].host != "uploads.example.com" || hostFiltered[0].ports[0] != 8443 {
		t.Fatalf("host filtered targets = %+v", hostFiltered)
	}
	portFiltered := endpointTargets(nil, conns, []int{443})
	if len(portFiltered) != 1 || portFiltered[0].host != "api.example.com" || portFiltered[0].ports[0] != 443 {
		t.Fatalf("port filtered targets = %+v", portFiltered)
	}
}

func TestEndpointTargetsUseExplicitHostWhenNoConnections(t *testing.T) {
	targets := endpointTargets([]string{"api.example.com"}, nil, nil)
	if len(targets) != 1 || targets[0].host != "api.example.com" || targets[0].ports[0] != 443 {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestParseDigStatuses(t *testing.T) {
	ok := parseDig(digNOERRORFixture)
	if ok.Status != "NOERROR" || ok.QueryMS != 7 || ok.Server != "1.1.1.1#53(1.1.1.1)" || len(ok.Addresses) != 1 {
		t.Fatalf("NOERROR dig = %+v", ok)
	}
	nx := parseDig(";; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN, id: 1\n;; Query time: 5 msec\n;; SERVER: 10.0.0.2#53(10.0.0.2)\n")
	if nx.Status != "NXDOMAIN" || len(nx.Addresses) != 0 {
		t.Fatalf("NXDOMAIN dig = %+v", nx)
	}
}

func TestLatencyAndZscalerFindings(t *testing.T) {
	report := Report{
		Network: netstateStateWithProxy("gateway.zscloud.net:8080"),
		Endpoints: []EndpointDiagnosis{{
			Host: "api.example.com",
			DNS:  DNSProbe{OK: true, Status: "NOERROR", QueryMS: slowDNSMS},
			Ports: []PortDiagnosis{{
				Port: 443,
				TCP:  TCPProbe{OK: true, DurationMS: slowTCPMS},
				TLS:  &TLSProbe{OK: true, DurationMS: slowTLSMS, ZscalerHint: true},
			}},
		}},
	}
	got := findings(report)
	for _, title := range []string{"zscaler-like proxy configured", "slow dns lookup", "slow tcp connect", "slow tls handshake", "zscaler tls issuer/subject observed"} {
		if !hasFinding(got, title) {
			t.Fatalf("missing finding %q in %+v", title, got)
		}
	}
}

func hasFinding(findings []Finding, title string) bool {
	for _, f := range findings {
		if f.Title == title {
			return true
		}
	}
	return false
}

func netstateStateWithProxy(https string) netstate.State {
	return netstate.State{Proxy: netstate.ProxyConfig{HTTPS: https}}
}

func fakeCommand(name string, args ...string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

const digNOERRORFixture = `; <<>> DiG 9.10 <<>> api.example.com
;; ->>HEADER<<- opcode: QUERY, status: NOERROR, id: 123
;; QUESTION SECTION:
;api.example.com. IN A
;; ANSWER SECTION:
api.example.com. 60 IN A 203.0.113.10
;; Query time: 7 msec
;; SERVER: 1.1.1.1#53(1.1.1.1)
`

func TestParseTraceroute(t *testing.T) {
	hops := parseTraceroute("1 192.0.2.1 1.2 ms\n2 * * *\n3 203.0.113.10 12.0 ms\n")
	if len(hops) != 3 || hops[1].TTL != 2 || !hops[1].Timeout || hops[2].Hosts[0] != "203.0.113.10" {
		t.Fatalf("hops = %+v", hops)
	}
}

type fakeDialer struct {
	err error
}

func (d fakeDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return fakeConn{}, nil
}

type fakeConn struct{}

func (fakeConn) Read([]byte) (int, error)         { return 0, nil }
func (fakeConn) Write([]byte) (int, error)        { return 0, nil }
func (fakeConn) Close() error                     { return nil }
func (fakeConn) LocalAddr() net.Addr              { return fakeAddr("local") }
func (fakeConn) RemoteAddr() net.Addr             { return fakeAddr("remote") }
func (fakeConn) SetDeadline(time.Time) error      { return nil }
func (fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return string(a) }
func (a fakeAddr) String() string  { return string(a) }

type fakeTLS struct {
	issuer string
	err    error
}

func (f fakeTLS) ProbeTLS(context.Context, string, int, time.Duration) (TLSProbe, error) {
	if f.err != nil {
		return TLSProbe{}, f.err
	}
	return TLSProbe{OK: true, Issuer: f.issuer, ZscalerHint: strings.Contains(strings.ToLower(f.issuer), "zscaler")}, nil
}
