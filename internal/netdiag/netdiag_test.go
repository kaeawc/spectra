package netdiag

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

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
	if len(report.Endpoints) != 1 || report.Endpoints[0].Host != "api.example.com" {
		t.Fatalf("endpoints = %+v", report.Endpoints)
	}
	if !report.Endpoints[0].DNS.OK || !report.Endpoints[0].Ports[0].TCP.OK {
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
		"nettop -P -L 2 -x -d -J bytes_in,bytes_out -t external": []byte(",bytes_in,bytes_out,\nSlack.412,100,50,\n"),
		"lsof -i -P -n": []byte("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nSlack 412 alice 29u IPv4 0xabc 0t0 TCP 192.0.2.10:55123->api.example.com:443 (ESTABLISHED)\n"),
		"dig +short +time=2 +tries=1 api.example.com": []byte("203.0.113.10\n"),
		"traceroute -n -m 12 -w 1 api.example.com":    []byte("1 192.0.2.1 1.0 ms\n2 203.0.113.10 10.0 ms\n"),
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

func fakeCommand(name string, args ...string) string {
	if len(args) == 0 {
		return name
	}
	return name + " " + strings.Join(args, " ")
}

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
