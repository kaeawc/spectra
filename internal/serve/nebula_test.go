package serve

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/rpc"
)

type fakeNebulaFactory struct {
	t *testing.T

	mu          sync.Mutex
	ready       chan struct{}
	cfg         NebulaConfig
	ln          net.Listener
	factoryErr  error
	listenErr   error
	closeCalled bool
	peer        *MeshPeer
}

func newFakeNebulaFactory(t *testing.T) *fakeNebulaFactory {
	t.Helper()
	return &fakeNebulaFactory{
		t:     t,
		ready: make(chan struct{}),
		peer: &MeshPeer{
			NodeName: "work-mac",
			Groups:   []string{"engineers"},
		},
	}
}

func (f *fakeNebulaFactory) newNode(cfg NebulaConfig) (MeshNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	if f.factoryErr != nil {
		return nil, f.factoryErr
	}
	return (*fakeNebulaNode)(f), nil
}

func (f *fakeNebulaFactory) config() NebulaConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fakeNebulaFactory) waitAddr(t *testing.T) string {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("fake nebula listener was not created")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		t.Fatal("fake nebula listener was not created")
	}
	return f.ln.Addr().String()
}

func (f *fakeNebulaFactory) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalled
}

type fakeNebulaNode fakeNebulaFactory

func (n *fakeNebulaNode) Listen(network string, _ string) (net.Listener, error) {
	f := (*fakeNebulaFactory)(n)
	if f.listenErr != nil {
		return nil, f.listenErr
	}
	ln, err := net.Listen(network, "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	close(f.ready)
	return ln, nil
}

func (n *fakeNebulaNode) Addrs() []netip.Addr {
	return []netip.Addr{netip.MustParseAddr("192.168.100.11")}
}

func (n *fakeNebulaNode) WhoIs(_ context.Context, _ string) (*MeshPeer, error) {
	f := (*fakeNebulaFactory)(n)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peer == nil {
		return nil, nil
	}
	peer := *f.peer
	return &peer, nil
}

func (n *fakeNebulaNode) Close() error {
	f := (*fakeNebulaFactory)(n)
	f.mu.Lock()
	f.closeCalled = true
	ln := f.ln
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	return nil
}

func TestRunNebulaUsesFactoryAndServesRPC(t *testing.T) {
	// Short tempdir: t.TempDir() paths exceed the macOS unix-socket limit.
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	dbPath := filepath.Join(dir, "t.db")
	configPath := filepath.Join(dir, "nebula.yaml")
	if err = os.WriteFile(configPath, []byte("pki: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newFakeNebulaFactory(t)
	logs := logger.NewCapture(slog.LevelInfo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			SockPath:          sockPath,
			DBPath:            dbPath,
			NebulaEnabled:     true,
			NebulaConfigPath:  configPath,
			NebulaAddr:        ":7880",
			NebulaAllowGroups: []string{"engineers"},
			NebulaFactory:     fake.newNode,
			SpectraVersion:    "test-version",
			CacheRegistry:     cache.Default,
			Logger:            logs,
		})
	}()

	addr := fake.waitAddr(t)
	conn, err := rpc.DialNetwork("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "health",
	}); err != nil {
		t.Fatal(err)
	}
	var resp rpc.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
	var health struct {
		OK     bool `json:"ok"`
		Nebula struct {
			Enabled    bool   `json:"enabled"`
			Provider   string `json:"provider"`
			ListenAddr string `json:"listen_addr"`
			IPv4       string `json:"ipv4"`
		} `json:"nebula"`
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &health); err != nil {
		t.Fatal(err)
	}
	if !health.Nebula.Enabled || health.Nebula.Provider != "nebula" || health.Nebula.ListenAddr != ":7880" {
		t.Fatalf("health nebula = %+v", health.Nebula)
	}
	if health.Nebula.IPv4 != "192.168.100.11" {
		t.Fatalf("health nebula IPv4 = %s", health.Nebula.IPv4)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}

	cfg := fake.config()
	if cfg.ConfigPath != configPath {
		t.Fatalf("config path = %q, want %q", cfg.ConfigPath, configPath)
	}
	if cfg.Version != "test-version" {
		t.Fatalf("version = %q, want test-version", cfg.Version)
	}
	if cfg.Logger == nil {
		t.Fatal("nebula config logger is nil")
	}
	if !fake.closed() {
		t.Fatal("fake nebula node was not closed")
	}
	waitForLog(t, logs, "daemon nebula peer connected")
}

func TestMeshPolicyAllowsGroups(t *testing.T) {
	policy := meshPolicy{AllowedGroups: []string{"Engineers"}}
	tests := []struct {
		name string
		peer *MeshPeer
		want bool
	}{
		{
			name: "group match is case-insensitive",
			peer: &MeshPeer{NodeName: "work-mac", Groups: []string{"engineers", "oncall"}},
			want: true,
		},
		{
			name: "no group match",
			peer: &MeshPeer{NodeName: "work-mac", Groups: []string{"contractors"}},
			want: false,
		},
		{
			name: "no groups at all",
			peer: &MeshPeer{NodeName: "work-mac"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := policy.Allows(tt.peer); got != tt.want {
				t.Fatalf("Allows = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNebulaIdentityListenerRejectsDisallowedGroup(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	logs := logger.NewCapture(slog.LevelInfo)
	fake := newFakeNebulaFactory(t)
	fake.peer = &MeshPeer{NodeName: "intruder-mac", Groups: []string{"contractors"}}
	node, err := fake.newNode(NebulaConfig{})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &meshIdentityListener{
		Listener: ln,
		node:     node,
		provider: "nebula",
		log:      logs,
		policy:   meshPolicy{AllowedGroups: []string{"engineers"}},
	}
	errCh := make(chan error, 1)
	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := wrapped.Accept()
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	waitForLog(t, logs, "daemon nebula peer rejected")
	_ = ln.Close()

	select {
	case conn := <-connCh:
		conn.Close()
		t.Fatal("disallowed peer was accepted")
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after listener close")
	}
}

func TestListenNebulaRequiresConfigPath(t *testing.T) {
	_, mesh, err := listenNebula(Options{NebulaEnabled: true}, logger.Discard())
	if err == nil {
		t.Fatal("listenNebula err = nil, want error")
	}
	if !strings.Contains(err.Error(), "nebula config") {
		t.Fatalf("listenNebula err = %v", err)
	}
	if mesh.node != nil || mesh.status != nil {
		t.Fatalf("listenNebula returned mesh on error: %+v", mesh)
	}
}

func TestListenNebulaClosesNodeOnListenError(t *testing.T) {
	wantErr := errors.New("listen failed")
	fake := newFakeNebulaFactory(t)
	fake.listenErr = wantErr

	ln, mesh, err := listenNebula(Options{
		NebulaConfigPath: "/tmp/nebula.yaml",
		NebulaFactory:    fake.newNode,
	}, logger.Discard())
	if err == nil {
		t.Fatal("listenNebula err = nil, want error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("listenNebula err = %v, want %v", err, wantErr)
	}
	if ln != nil || mesh.node != nil || mesh.status != nil {
		t.Fatalf("listenNebula returned values on error: %v %v %v", ln, mesh.node, mesh.status)
	}
	if !fake.closed() {
		t.Fatal("fake nebula node was not closed after Listen error")
	}
}

func TestListenNebulaFactoryError(t *testing.T) {
	wantErr := errors.New("bad config")
	fake := newFakeNebulaFactory(t)
	fake.factoryErr = wantErr

	_, _, err := listenNebula(Options{
		NebulaConfigPath: "/tmp/nebula.yaml",
		NebulaFactory:    fake.newNode,
	}, logger.Discard())
	if !errors.Is(err, wantErr) {
		t.Fatalf("listenNebula err = %v, want %v", err, wantErr)
	}
}
