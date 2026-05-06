package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/helperclient"
	"github.com/kaeawc/spectra/internal/netcap"
	"github.com/kaeawc/spectra/internal/netdiag"
	"github.com/kaeawc/spectra/internal/netproto"
	"github.com/kaeawc/spectra/internal/netstate"
)

func TestRunNetworkCaptureStartCallsHelper(t *testing.T) {
	restore := stubNetworkCaptureStart(t, func(iface string, durationMS, snapLen int, proto, host string, port int) (map[string]any, error) {
		if iface != "en0" || durationMS != 5000 || snapLen != 4096 || proto != "tcp" || host != "api.example.com" || port != 443 {
			t.Fatalf("params = %q %d %d %q %q %d", iface, durationMS, snapLen, proto, host, port)
		}
		return map[string]any{
			"handle":      "netcap-1",
			"interface":   iface,
			"duration_ms": durationMS,
			"output_path": "/var/tmp/spectra-netcap/501/netcap-1.pcap",
		}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "start", "--interface", "en0", "--duration", "5s", "--snaplen", "4096", "--proto", "tcp", "--host", "api.example.com", "--port", "443"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	if !strings.Contains(out, "capture started") || !strings.Contains(out, "handle: netcap-1") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunNetworkCaptureStartJSON(t *testing.T) {
	restore := stubNetworkCaptureStart(t, func(string, int, int, string, string, int) (map[string]any, error) {
		return map[string]any{"handle": "netcap-2"}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "start", "--interface", "utun3", "--json"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	if !strings.Contains(out, `"handle": "netcap-2"`) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunNetworkCaptureStopCallsHelper(t *testing.T) {
	restore := stubNetworkCaptureStop(t, func(handle string) (map[string]any, error) {
		if handle != "netcap-1" {
			t.Fatalf("handle = %q, want netcap-1", handle)
		}
		return map[string]any{"handle": handle, "size_bytes": 128}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "stop", "netcap-1"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	if !strings.Contains(out, "capture stopped") || !strings.Contains(out, "size_bytes: 128") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunNetworkCaptureStopSummarizesOutputPath(t *testing.T) {
	restoreStop := stubNetworkCaptureStop(t, func(handle string) (map[string]any, error) {
		if handle != "netcap-1" {
			t.Fatalf("handle = %q, want netcap-1", handle)
		}
		return map[string]any{"handle": handle, "output_path": "/tmp/capture.pcap", "size_bytes": 128}, nil
	})
	defer restoreStop()
	restoreSummary := stubNetworkCaptureSummarize(t, func(path string, limit int) (netcap.PCAPSummary, error) {
		if path != "/tmp/capture.pcap" || limit != 3 {
			t.Fatalf("summary params = %q %d", path, limit)
		}
		return netcap.PCAPSummary{Packets: 2, DecodedFlows: 1}, nil
	})
	defer restoreSummary()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "stop", "--summarize", "--limit", "3", "netcap-1"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{"capture stopped", "output_path: /tmp/capture.pcap", "capture summary", "packets: 2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRunNetworkCaptureStopSummarizesJSON(t *testing.T) {
	restoreStop := stubNetworkCaptureStop(t, func(string) (map[string]any, error) {
		return map[string]any{"handle": "netcap-1", "output_path": "/tmp/capture.pcap"}, nil
	})
	defer restoreStop()
	restoreSummary := stubNetworkCaptureSummarize(t, func(string, int) (netcap.PCAPSummary, error) {
		return netcap.PCAPSummary{Packets: 2}, nil
	})
	defer restoreSummary()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "stop", "--summarize", "--json", "netcap-1"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{`"capture":`, `"summary":`, `"packets": 2`} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRunNetworkCaptureSummarize(t *testing.T) {
	restore := stubNetworkCaptureSummarize(t, func(path string, limit int) (netcap.PCAPSummary, error) {
		if path != "/tmp/capture.pcap" || limit != 2 {
			t.Fatalf("params = %q %d", path, limit)
		}
		return netcap.PCAPSummary{
			Packets:      3,
			DecodedFlows: 3,
			DNS: []netcap.DNSFlowSummary{{
				Flow:    netcap.FlowSummary{SrcAddr: "192.0.2.10", SrcPort: 53123, DstAddr: "198.51.100.20", DstPort: 53},
				Message: netproto.DNSMessage{Questions: []netproto.DNSQuestion{{Name: "example.com", Type: "A", Class: "IN"}}},
			}},
			TLS: []netcap.TLSFlowSummary{{
				Flow:        netcap.FlowSummary{SrcAddr: "192.0.2.10", SrcPort: 53124, DstAddr: "198.51.100.20", DstPort: 443},
				ClientHello: netproto.TLSClientHello{SNI: "example.com"},
			}},
			HTTP: []netcap.HTTPFlowSummary{{
				Flow:    netcap.FlowSummary{SrcAddr: "192.0.2.10", SrcPort: 53125, DstAddr: "198.51.100.20", DstPort: 80},
				Message: netproto.HTTPMessage{IsRequest: true, Method: "GET", Target: "/chat", WebSocket: true},
			}},
		}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "summarize", "--limit", "2", "/tmp/capture.pcap"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{"capture summary", "packets: 3", "websocket_upgrades: 1", "dns_query: example.com A", "tls_client_hello: example.com", "websocket_upgrade: GET /chat"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestRunNetworkCaptureSummarizeJSON(t *testing.T) {
	restore := stubNetworkCaptureSummarize(t, func(string, int) (netcap.PCAPSummary, error) {
		return netcap.PCAPSummary{Packets: 1, DecodedFlows: 1}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"capture", "summary", "--json", "/tmp/capture.pcap"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	if !strings.Contains(out, `"packets": 1`) {
		t.Fatalf("stdout = %q", out)
	}
}

func TestRunNetworkCaptureRequiresInterface(t *testing.T) {
	restore := stubNetworkCaptureStart(t, func(string, int, int, string, string, int) (map[string]any, error) {
		t.Fatal("helper should not be called")
		return nil, nil
	})
	defer restore()

	if code := runNetwork([]string{"capture", "start"}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestRunNetworkCaptureUnavailableHelper(t *testing.T) {
	restore := stubNetworkCaptureStart(t, func(string, int, int, string, string, int) (map[string]any, error) {
		return nil, helperclient.ErrHelperUnavailable
	})
	defer restore()

	if code := runNetwork([]string{"capture", "start", "--interface", "en0"}); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestRunNetworkDiagnose(t *testing.T) {
	restore := stubNetworkDiagnose(t, func(_ context.Context, opts netdiag.Options) (netdiag.Report, error) {
		if opts.AppPath != "/Applications/Slack.app" || opts.PID != 412 || len(opts.Targets) != 0 {
			t.Fatalf("opts = %+v", opts)
		}
		if len(opts.Ports) != 0 {
			t.Fatalf("ports = %+v", opts.Ports)
		}
		return netdiag.Report{
			AppPath: "/Applications/Slack.app",
			Network: netstate.State{
				DefaultRouteIface: "en0",
				DefaultRouteGW:    "192.0.2.1",
				DNSServers:        []string{"1.1.1.1"},
				VPNActive:         true,
				VPNInterfaces:     []string{"utun4"},
			},
			Processes:   []netdiag.ProcessSummary{{PID: 412, Command: "Slack", ExecutablePath: "/Applications/Slack.app/Contents/MacOS/Slack"}},
			Connections: []netstate.Connection{{PID: 412, Proto: "tcp", State: "established", LocalAddr: "192.0.2.10:50000", RemoteAddr: "api.example.com:443"}},
			Throughput:  []netstate.Throughput{{PID: 412, Command: "Slack", BytesInPerSec: 100, BytesOutPerSec: 50}},
			TopThroughput: []netstate.Throughput{
				{PID: 900, Command: "backupd", BytesInPerSec: 1000, BytesOutPerSec: 1000},
				{PID: 412, Command: "Slack", BytesInPerSec: 100, BytesOutPerSec: 50},
			},
			Endpoints: []netdiag.EndpointDiagnosis{{
				Host:       "api.example.com",
				DNS:        netdiag.DNSProbe{OK: true, Addresses: []string{"203.0.113.10"}},
				Traceroute: netdiag.TraceProbe{OK: true, Hops: []netdiag.TraceHop{{TTL: 1}}},
				Ports:      []netdiag.PortDiagnosis{{Port: 443, TCP: netdiag.TCPProbe{OK: true}}},
			}},
			Findings: []netdiag.Finding{{Severity: "info", Title: "vpn/tunnel interfaces active", Detail: "utun4"}},
		}, nil
	})
	defer restore()

	out := captureStdout(t, func() {
		code := runNetwork([]string{"diagnose", "--app", "/Applications/Slack.app", "--pid", "412"})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	})
	for _, want := range []string{"Network diagnosis", "app:      /Applications/Slack.app", "active app connections", "top network consumers", "endpoint probes", "vpn/tunnel interfaces active"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func stubNetworkCaptureStart(t *testing.T, fn func(string, int, int, string, string, int) (map[string]any, error)) func() {
	t.Helper()
	old := networkCaptureStart
	networkCaptureStart = fn
	return func() { networkCaptureStart = old }
}

func stubNetworkCaptureStop(t *testing.T, fn func(string) (map[string]any, error)) func() {
	t.Helper()
	old := networkCaptureStop
	networkCaptureStop = fn
	return func() { networkCaptureStop = old }
}

func stubNetworkCaptureSummarize(t *testing.T, fn func(string, int) (netcap.PCAPSummary, error)) func() {
	t.Helper()
	old := networkCaptureSummarize
	networkCaptureSummarize = fn
	return func() { networkCaptureSummarize = old }
}

func stubNetworkDiagnose(t *testing.T, fn func(context.Context, netdiag.Options) (netdiag.Report, error)) func() {
	t.Helper()
	old := networkDiagnose
	networkDiagnose = fn
	return func() { networkDiagnose = old }
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	if err := w.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
