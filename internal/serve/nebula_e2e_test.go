package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/cert"
	"github.com/slackhq/nebula/cert_test"
	nebulaconfig "github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
	yaml "go.yaml.in/yaml/v3"
)

// The end-to-end tests below build a real two-node Nebula mesh over loopback
// UDP: the daemon side goes through the production NewNebulaServer path (config
// file, embedded node, identity listener), and the client side is a raw nebula
// service node dialing the daemon's overlay address. No tun device, no root,
// no external processes.

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	_ = pc.Close()
	return port
}

func nebulaTestConfig(t *testing.T, ca cert.Certificate, caKey []byte, name string, ip netip.Addr, groups []string, overrides map[string]any) map[string]any {
	t.Helper()
	_, _, keyPEM, certPEM := cert_test.NewTestCert(
		cert.Version2, cert.Curve_CURVE25519, ca, caKey, name,
		time.Now().Add(-time.Minute), time.Now().Add(10*time.Minute),
		[]netip.Prefix{netip.PrefixFrom(ip, 24)}, nil, groups,
	)
	caPEM, err := ca.MarshalPEM()
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"pki": map[string]any{
			"ca":   string(caPEM),
			"cert": string(certPEM),
			"key":  string(keyPEM),
		},
		"firewall": map[string]any{
			"outbound": []map[string]any{{"proto": "any", "port": "any", "host": "any"}},
			"inbound":  []map[string]any{{"proto": "any", "port": "any", "host": "any"}},
		},
		"timers": map[string]any{
			"pending_deletion_interval": 2,
			"connection_alive_interval": 2,
		},
		"handshakes": map[string]any{"try_interval": "200ms"},
	}
	for k, v := range overrides {
		cfg[k] = v
	}
	return cfg
}

func writeNebulaTestConfig(t *testing.T, dir string, cfg map[string]any) string {
	t.Helper()
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nebula.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func startNebulaTestClient(t *testing.T, cfg map[string]any) *service.Service {
	t.Helper()
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var c nebulaconfig.C
	if err := c.LoadString(string(raw)); err != nil {
		t.Fatal(err)
	}
	control, err := nebula.Main(&c, false, "spectra-test-client", slog.New(slog.DiscardHandler), overlay.NewUserDeviceFromConfig)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(control)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

type nebulaE2E struct {
	listener  net.Listener
	mesh      meshRuntime
	logs      *logger.Capture
	daemonIP  netip.Addr
	newClient func(t *testing.T, name string, groups []string) *service.Service
	closeOnce sync.Once
}

// closeListener closes the daemon-side listener exactly once; nebula's
// tcpListener panics on a second Close.
func (e *nebulaE2E) closeListener() {
	e.closeOnce.Do(func() { _ = e.listener.Close() })
}

// startNebulaE2E stands up the daemon side through the production
// listenNebula path (lighthouse on a loopback UDP port) and returns a factory
// for mesh clients that dial it.
func startNebulaE2E(t *testing.T, allowGroups []string) *nebulaE2E {
	t.Helper()
	ca, _, caKey, _ := cert_test.NewTestCaCert(
		cert.Version2, cert.Curve_CURVE25519,
		time.Now().Add(-time.Minute), time.Now().Add(time.Hour),
		nil, nil, []string{},
	)
	daemonIP := netip.MustParseAddr("10.99.0.1")
	daemonUDP := freeUDPPort(t)

	daemonCfg := nebulaTestConfig(t, ca, caKey, "spectra-daemon", daemonIP, []string{"daemons"}, map[string]any{
		"static_host_map": map[string]any{},
		"lighthouse":      map[string]any{"am_lighthouse": true},
		"listen":          map[string]any{"host": "127.0.0.1", "port": daemonUDP},
	})
	configPath := writeNebulaTestConfig(t, t.TempDir(), daemonCfg)

	logs := logger.NewCapture(slog.LevelInfo)
	ln, mesh, err := listenNebula(Options{
		NebulaConfigPath:  configPath,
		NebulaAddr:        ":7878",
		NebulaAllowGroups: allowGroups,
		SpectraVersion:    "e2e-test",
		Logger:            logs,
	}, logs)
	if err != nil {
		t.Fatal(err)
	}
	e2e := &nebulaE2E{listener: ln, mesh: mesh, logs: logs, daemonIP: netip.MustParseAddr("10.99.0.1")}
	t.Cleanup(func() {
		e2e.closeListener()
		_ = mesh.node.Close()
	})

	nextIP := netip.MustParseAddr("10.99.0.2")
	newClient := func(t *testing.T, name string, groups []string) *service.Service {
		clientCfg := nebulaTestConfig(t, ca, caKey, name, nextIP, groups, map[string]any{
			"static_host_map": map[string]any{
				daemonIP.String(): []string{fmt.Sprintf("127.0.0.1:%d", daemonUDP)},
			},
			"lighthouse": map[string]any{
				"hosts":    []string{daemonIP.String()},
				"interval": 1,
			},
		})
		nextIP = nextIP.Next()
		return startNebulaTestClient(t, clientCfg)
	}
	e2e.newClient = newClient
	return e2e
}

func TestNebulaEndToEndAllowsPeerByGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("real nebula mesh handshake; skipped in -short")
	}
	e2e := startNebulaE2E(t, []string{"engineers"})

	if addrs := e2e.mesh.node.Addrs(); !slices.Contains(addrs, e2e.daemonIP) {
		t.Fatalf("daemon overlay addrs = %v, want to contain %v", addrs, e2e.daemonIP)
	}

	greeted := make(chan error, 1)
	go func() {
		conn, err := e2e.listener.Accept()
		if err != nil {
			greeted <- err
			return
		}
		defer conn.Close()
		_, err = conn.Write([]byte("spectra"))
		greeted <- err
	}()

	client := e2e.newClient(t, "alice-mac", []string{"engineers"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", net.JoinHostPort(e2e.daemonIP.String(), "7878"))
	if err != nil {
		t.Fatalf("dial daemon over mesh: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read greeting over mesh: %v", err)
	}
	if got := string(buf[:n]); got != "spectra" {
		t.Fatalf("greeting = %q, want spectra", got)
	}
	if err := <-greeted; err != nil {
		t.Fatalf("daemon accept/write: %v", err)
	}
	waitForLog(t, e2e.logs, "daemon nebula peer connected")
}

func TestNebulaEndToEndRejectsPeerWithoutGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("real nebula mesh handshake; skipped in -short")
	}
	e2e := startNebulaE2E(t, []string{"engineers"})

	acceptErr := make(chan error, 1)
	go func() {
		// The identity gate should reject the peer before Accept returns a
		// connection; Accept unblocks with an error when the listener closes
		// at test cleanup.
		conn, err := e2e.listener.Accept()
		if err == nil {
			conn.Close()
			acceptErr <- errors.New("disallowed peer was accepted")
			return
		}
		acceptErr <- nil
	}()

	client := e2e.newClient(t, "intruder-mac", []string{"contractors"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", net.JoinHostPort(e2e.daemonIP.String(), "7878"))
	if err == nil {
		// The TCP handshake may complete inside the netstack before the
		// daemon-side gate closes the connection; the close must then surface
		// on the first read.
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 1)
		if _, rerr := conn.Read(buf); rerr == nil {
			t.Fatal("read succeeded from a peer the policy should reject")
		} else if !errors.Is(rerr, io.EOF) && !isConnReset(rerr) {
			t.Logf("read from rejected peer returned %v (acceptable: connection was closed)", rerr)
		}
		_ = conn.Close()
	}
	waitForLog(t, e2e.logs, "daemon nebula peer rejected")

	e2e.closeListener()
	select {
	case err := <-acceptErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept goroutine did not finish")
	}
}

func isConnReset(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, net.ErrClosed)
}
