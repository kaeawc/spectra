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
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kaeawc/spectra/internal/artifact"
	"github.com/kaeawc/spectra/internal/cache"
	"github.com/kaeawc/spectra/internal/jvm"
	"github.com/kaeawc/spectra/internal/livehistory"
	"github.com/kaeawc/spectra/internal/logger"
	"github.com/kaeawc/spectra/internal/metrics"
	"github.com/kaeawc/spectra/internal/rpc"
	"github.com/kaeawc/spectra/internal/snapshot"
	"github.com/kaeawc/spectra/internal/store"
	"github.com/kaeawc/spectra/internal/toolchain"
)

// testDaemon wires up a dispatcher with registered handlers, starts it on a
// temp Unix socket, and returns a connected RPC client.
func testDaemon(t *testing.T) (*json.Encoder, *json.Decoder, context.CancelFunc) {
	enc, dec, _, cancel := testDaemonWithDB(t)
	return enc, dec, cancel
}

func testDaemonWithDB(t *testing.T) (*json.Encoder, *json.Decoder, *store.DB, context.CancelFunc) {
	t.Helper()
	// Use os.MkdirTemp with a short prefix under /tmp: macOS limits Unix socket
	// paths to 104 bytes and t.TempDir() embeds the full test name which can
	// exceed that limit for long test names.
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	dbPath := filepath.Join(dir, "t.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	d := rpc.NewDispatcher()
	origCollectToolchains := collectToolchains
	origCollectJDKs := collectJDKs
	collectToolchains = func(context.Context, toolchain.CollectOptions) toolchain.Toolchains {
		return toolchain.Toolchains{
			Brew: toolchain.BrewInventory{
				Formulae: []toolchain.BrewFormula{},
				Casks:    []toolchain.BrewCask{},
				Taps:     []toolchain.BrewTap{},
			},
			Node:       []toolchain.RuntimeInstall{},
			Python:     []toolchain.RuntimeInstall{},
			Go:         []toolchain.RuntimeInstall{},
			Ruby:       []toolchain.RuntimeInstall{},
			Rust:       []toolchain.RustToolchain{},
			JDKs:       []toolchain.JDKInstall{},
			BuildTools: []toolchain.BuildTool{},
		}
	}
	collectJDKs = func(context.Context, toolchain.CollectOptions) []toolchain.JDKInstall {
		return nil
	}
	t.Cleanup(func() {
		collectToolchains = origCollectToolchains
		collectJDKs = origCollectJDKs
	})
	registerHandlers(d, "test-version", db, metrics.NewCollector(), livehistory.NewRing(livehistory.DefaultCapacity), cache.Default, nil, &artifact.FakeRecorder{}, nil, nil)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-ctx.Done(); ln.Close() }()
	go d.ServeListener(ln)
	time.Sleep(10 * time.Millisecond)

	conn, err := rpc.DialUnix(sockPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := conn.(interface{ SetDeadline(time.Time) error }).SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	return json.NewEncoder(conn), json.NewDecoder(conn), db, cancel
}

func TestDaemonHealthEndpoint(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	if err := enc.Encode(req{JSONRPC: "2.0", ID: 1, Method: "health"}); err != nil {
		t.Fatal(err)
	}
	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["ok"] != true {
		t.Errorf("ok = %v, want true", m["ok"])
	}
	if m["version"] != "test-version" {
		t.Errorf("version = %v, want test-version", m["version"])
	}
}

func TestRunLogsLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	dbPath := filepath.Join(dir, "t.db")
	logs := logger.NewCapture(slog.LevelInfo)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			SockPath:       sockPath,
			DBPath:         dbPath,
			SpectraVersion: "test-version",
			CacheRegistry:  cache.Default,
			Logger:         logs,
		})
	}()

	waitForSocket(t, sockPath)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
	if !logs.HasMessage("daemon unix listener ready") {
		t.Fatalf("missing listener log: %+v", logs.Records())
	}
	if !logs.HasMessage("daemon storage ready") {
		t.Fatalf("missing storage log: %+v", logs.Records())
	}
	if !logs.HasMessage("daemon stopped") {
		t.Fatalf("missing stopped log: %+v", logs.Records())
	}
}

func TestRunTsnetUsesFactoryAndServesRPC(t *testing.T) {
	dir, err := os.MkdirTemp("", "sp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	dbPath := filepath.Join(dir, "t.db")
	stateDir := filepath.Join(dir, "tsnet")
	fake := newFakeTsnetFactory(t)
	logs := logger.NewCapture(slog.LevelInfo)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Options{
			SockPath:         sockPath,
			DBPath:           dbPath,
			TsnetEnabled:     true,
			TsnetAddr:        ":7879",
			TsnetHostname:    "Work Mac.local",
			TsnetStateDir:    stateDir,
			TsnetEphemeral:   true,
			TsnetTags:        []string{"tag:engineer", "tag:spectra"},
			TsnetAllowLogins: []string{"alice@example.com"},
			TsnetAllowNodes:  []string{"alice-mac.tailnet.ts.net"},
			TsnetFactory:     fake.newNode,
			SpectraVersion:   "test-version",
			CacheRegistry:    cache.Default,
			Logger:           logs,
		})
	}()

	addr := fake.waitAddr(t)
	conn, err := rpc.DialNetwork("tcp", addr)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "health",
	}); err != nil {
		cancel()
		t.Fatal(err)
	}
	var resp rpc.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.Error != nil {
		cancel()
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
		Tsnet   struct {
			Enabled    bool   `json:"enabled"`
			Hostname   string `json:"hostname"`
			ListenAddr string `json:"listen_addr"`
			IPv4       string `json:"ipv4"`
			IPv6       string `json:"ipv6"`
		} `json:"tsnet"`
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &health); err != nil {
		cancel()
		t.Fatal(err)
	}
	if !health.Tsnet.Enabled || health.Tsnet.Hostname != "work-mac" || health.Tsnet.ListenAddr != ":7879" {
		cancel()
		t.Fatalf("health tsnet = %+v", health.Tsnet)
	}
	if health.Tsnet.IPv4 != "100.64.0.10" || health.Tsnet.IPv6 != "fd7a:115c:a1e0::10" {
		cancel()
		t.Fatalf("health tsnet IPs = %s %s", health.Tsnet.IPv4, health.Tsnet.IPv6)
	}
	fake.waitWhoIs(t)

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
	if cfg.StateDir != stateDir {
		t.Fatalf("state dir = %q, want %q", cfg.StateDir, stateDir)
	}
	if cfg.Hostname != "work-mac" {
		t.Fatalf("hostname = %q, want work-mac", cfg.Hostname)
	}
	if !cfg.Ephemeral {
		t.Fatal("ephemeral = false, want true")
	}
	if !slices.Equal(cfg.Tags, []string{"tag:engineer", "tag:spectra"}) {
		t.Fatalf("tags = %v", cfg.Tags)
	}
	if !slices.Equal(cfg.AllowLogins, []string{"alice@example.com"}) {
		t.Fatalf("allow logins = %v", cfg.AllowLogins)
	}
	if !slices.Equal(cfg.AllowNodes, []string{"alice-mac.tailnet.ts.net"}) {
		t.Fatalf("allow nodes = %v", cfg.AllowNodes)
	}
	if cfg.UserLogf == nil {
		t.Fatal("UserLogf is nil")
	}
	if !fake.closed() {
		t.Fatal("fake tsnet node was not closed")
	}
	waitForLog(t, logs, "daemon tsnet peer connected")
	if got := fake.whoIsRemoteAddr(); got == "" {
		t.Fatal("WhoIs was not called with a remote address")
	}
	info, err := os.Stat(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", got)
	}
}

func TestTsnetPolicyAllowsLoginOrNode(t *testing.T) {
	policy := tsnetPolicy{
		AllowedLogins: []string{"alice@example.com"},
		AllowedNodes:  []string{"work-mac.tailnet.ts.net"},
	}
	tests := []struct {
		name string
		peer *TsnetPeer
		want bool
	}{
		{
			name: "login match",
			peer: &TsnetPeer{LoginName: "Alice@Example.com", NodeName: "other.tailnet.ts.net."},
			want: true,
		},
		{
			name: "node match",
			peer: &TsnetPeer{LoginName: "bob@example.com", NodeName: "work-mac.tailnet.ts.net."},
			want: true,
		},
		{
			name: "no match",
			peer: &TsnetPeer{LoginName: "bob@example.com", NodeName: "other.tailnet.ts.net."},
			want: false,
		},
		{
			name: "nil peer",
			peer: nil,
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

func TestTsnetIdentityListenerRejectsDisallowedPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	logs := logger.NewCapture(slog.LevelInfo)
	fake := newFakeTsnetFactory(t)
	fake.peer = TsnetPeer{
		LoginName:   "bob@example.com",
		DisplayName: "Bob Example",
		NodeName:    "bob-mac.tailnet.ts.net.",
	}
	wrapped := &tsnetIdentityListener{
		Listener: ln,
		node:     fake.newNode(TsnetConfig{}),
		log:      logs,
		policy:   tsnetPolicy{AllowedLogins: []string{"alice@example.com"}},
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
	fake.waitWhoIs(t)
	_ = ln.Close()

	select {
	case conn := <-connCh:
		conn.Close()
		t.Fatal("disallowed peer was accepted")
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not return after listener close")
	}
	waitForLog(t, logs, "daemon tsnet peer rejected")
}

func TestListenTsnetClosesNodeOnListenError(t *testing.T) {
	dir := t.TempDir()
	wantErr := errors.New("listen failed")
	fake := newFakeTsnetFactory(t)
	fake.listenErr = wantErr

	ln, node, status, err := listenTsnet(Options{
		TsnetAddr:     ":7879",
		TsnetHostname: "work-mac",
		TsnetStateDir: dir,
		TsnetFactory:  fake.newNode,
	}, logger.Discard())
	if err == nil {
		t.Fatal("listenTsnet err = nil, want error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("listenTsnet err = %v, want %v", err, wantErr)
	}
	if ln != nil || node != nil || status != nil {
		t.Fatalf("listenTsnet returned values on error: %v %v %v", ln, node, status)
	}
	if !fake.closed() {
		t.Fatal("fake tsnet node was not closed after Listen error")
	}
}

func TestSanitizeTsnetHostname(t *testing.T) {
	tests := map[string]string{
		"Work Mac.local": "work-mac",
		"_Spectra__":     "spectra",
		"bad!name":       "bad-name",
		"---":            "",
	}
	for in, want := range tests {
		if got := sanitizeTsnetHostname(in); got != want {
			t.Fatalf("sanitizeTsnetHostname(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeTsnetFactory struct {
	t *testing.T

	mu          sync.Mutex
	ready       chan struct{}
	whois       chan struct{}
	cfg         TsnetConfig
	ln          net.Listener
	listenErr   error
	closeCalled bool
	whoisCalled bool
	remoteAddr  string
	peer        TsnetPeer
}

func newFakeTsnetFactory(t *testing.T) *fakeTsnetFactory {
	t.Helper()
	return &fakeTsnetFactory{
		t:     t,
		ready: make(chan struct{}),
		whois: make(chan struct{}),
		peer: TsnetPeer{
			LoginName:   "alice@example.com",
			DisplayName: "Alice Example",
			NodeName:    "alice-mac.tailnet.ts.net.",
		},
	}
}

func (f *fakeTsnetFactory) newNode(cfg TsnetConfig) TsnetNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	return (*fakeTsnetNode)(f)
}

func (f *fakeTsnetFactory) config() TsnetConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fakeTsnetFactory) waitAddr(t *testing.T) string {
	t.Helper()
	select {
	case <-f.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("fake tsnet listener was not created")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ln == nil {
		t.Fatal("fake tsnet listener was not created")
	}
	return f.ln.Addr().String()
}

func (f *fakeTsnetFactory) closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalled
}

func (f *fakeTsnetFactory) waitWhoIs(t *testing.T) {
	t.Helper()
	select {
	case <-f.whois:
	case <-time.After(5 * time.Second):
		t.Fatal("fake tsnet WhoIs was not called")
	}
}

func (f *fakeTsnetFactory) whoIsRemoteAddr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remoteAddr
}

type fakeTsnetNode fakeTsnetFactory

func (n *fakeTsnetNode) Listen(network string, _ string) (net.Listener, error) {
	f := (*fakeTsnetFactory)(n)
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

func (n *fakeTsnetNode) TailscaleIPs() (netip.Addr, netip.Addr) {
	return netip.MustParseAddr("100.64.0.10"), netip.MustParseAddr("fd7a:115c:a1e0::10")
}

func (n *fakeTsnetNode) WhoIs(_ context.Context, remoteAddr string) (*TsnetPeer, error) {
	f := (*fakeTsnetFactory)(n)
	f.mu.Lock()
	f.remoteAddr = remoteAddr
	if !f.whoisCalled {
		f.whoisCalled = true
		close(f.whois)
	}
	peer := f.peer
	f.mu.Unlock()
	return &peer, nil
}

func (n *fakeTsnetNode) Close() error {
	f := (*fakeTsnetFactory)(n)
	f.mu.Lock()
	f.closeCalled = true
	ln := f.ln
	f.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	return nil
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s was not created", path)
}

func waitForLog(t *testing.T, logs *logger.Capture, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if logs.HasMessage(msg) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing log %q: %+v", msg, logs.Records())
}

func TestDaemonSnapshotList(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 2, Method: "snapshot.list"})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	// Empty DB → result should be nil or empty slice.
}

func TestDaemonInspectAppMissingPath(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Missing path → should return an RPC error, not crash.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 3, Method: "inspect.app", Params: json.RawMessage(`{}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for missing path, got nil")
	}
}

func TestDaemonInspectAppBatchEmpty(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Empty paths → should return an RPC error.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 4, Method: "inspect.app.batch", Params: json.RawMessage(`{"paths":[]}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for empty paths, got nil")
	}
}

func TestDaemonInspectHost(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 5, Method: "inspect.host"})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	// HostInfo should have at least a hostname field.
	if m["hostname"] == nil && m["Hostname"] == nil {
		t.Error("inspect.host: expected hostname in result")
	}
}

func rpcCall(t *testing.T, enc *json.Encoder, dec *json.Decoder, id int, method string, params string) rpc.Response {
	t.Helper()
	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	_ = enc.Encode(req{JSONRPC: "2.0", ID: id, Method: method, Params: json.RawMessage(params)})
	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("rpcCall %s: decode: %v", method, err)
	}
	return resp
}

func TestDaemonIssuesListMissingMachine(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 10, "issues.list", `{}`)
	if resp.Error == nil {
		t.Error("expected error when machine_uuid missing")
	}
}

func TestDaemonIssuesRecordEmpty(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	// issues.record with empty findings should succeed (0 upserts).
	resp := rpcCall(t, enc, dec, 11, "issues.record",
		`{"machine_uuid":"TEST-1","snapshot_id":"snap-X","findings":[]}`)
	if resp.Error != nil {
		t.Fatalf("issues.record (empty): %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", resp.Result)
	}
	if m["upserted"].(float64) != 0 {
		t.Errorf("upserted = %v, want 0", m["upserted"])
	}
}

func TestDaemonIssuesCheckFromSnapshot(t *testing.T) {
	enc, dec, db, cancel := testDaemonWithDB(t)
	defer cancel()

	snapshotID := "snap-test-issues-check"
	input := store.FromSnapshot(snapshot.Snapshot{
		ID:      snapshotID,
		TakenAt: time.Now().UTC(),
		Kind:    snapshot.KindLive,
		Host:    snapshot.HostInfo{MachineUUID: "test-machine", Hostname: "test.local", OSName: "macOS"},
	})
	if err := db.SaveSnapshot(context.Background(), input); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := db.SaveSnapshotProcesses(context.Background(), snapshotID, nil); err != nil {
		t.Fatalf("seed snapshot processes: %v", err)
	}
	if err := db.SaveLoginItems(context.Background(), snapshotID, nil); err != nil {
		t.Fatalf("seed snapshot login items: %v", err)
	}
	if err := db.SaveGrantedPerms(context.Background(), snapshotID, nil); err != nil {
		t.Fatalf("seed snapshot granted perms: %v", err)
	}

	resp := rpcCall(t, enc, dec, 16, "issues.check", `{"snapshot_id":"`+snapshotID+`"}`)
	if resp.Error != nil {
		t.Fatalf("issues.check: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", resp.Result)
	}
	if got, ok := m["snapshot_id"].(string); !ok || got != snapshotID {
		t.Fatalf("snapshot_id = %v, want %q", m["snapshot_id"], snapshotID)
	}
	if m["machine_uuid"] == nil || m["machine_uuid"].(string) == "" {
		t.Fatal("issues.check: missing machine_uuid")
	}
	if _, ok := m["upserted"].(float64); !ok {
		t.Fatalf("upserted type %T", m["upserted"])
	}
}

func TestDaemonIssuesCheckMissingSnapshot(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	resp := rpcCall(t, enc, dec, 17, "issues.check", `{"snapshot_id":"does-not-exist"}`)
	if resp.Error == nil {
		t.Fatal("expected error for missing snapshot")
	}
}

func TestDaemonIssuesUpdateMissingStatus(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 12, "issues.update", `{"id":"x"}`)
	if resp.Error == nil {
		t.Error("expected error when status missing")
	}
}

func TestDaemonIssuesFixRecordMissingIssueID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 13, "issues.fix.record", `{}`)
	if resp.Error == nil {
		t.Error("expected error when issue_id missing")
	}
}

func TestDaemonIssuesFixListMissingIssueID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 14, "issues.fix.list", `{}`)
	if resp.Error == nil {
		t.Error("expected error when issue_id missing")
	}
}

func TestDaemonJDKListReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 16, "jdk.list", `{}`)
	if resp.Error != nil {
		t.Fatalf("jdk.list: %v", resp.Error)
	}
}

func TestDaemonToolchainScanReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 17, "toolchain.scan", `{}`)
	if resp.Error != nil {
		t.Fatalf("toolchain.scan: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["brew"] == nil {
		t.Error("toolchain.scan: expected brew field in result")
	}
}

func TestDaemonSnapshotDiffMissingIDs(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()

	type req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	// Missing id_b → should return an RPC error.
	_ = enc.Encode(req{JSONRPC: "2.0", ID: 6, Method: "snapshot.diff", Params: json.RawMessage(`{"id_a":"x"}`)})

	var resp rpc.Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Error("expected RPC error for missing id_b, got nil")
	}
}

func TestDaemonNetworkStateReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 18, "network.state", `{}`)
	if resp.Error != nil {
		t.Fatalf("network.state: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	// network.state should have vpn_active field.
	if _, exists := m["vpn_active"]; !exists {
		t.Error("network.state: expected vpn_active field in result")
	}
}

func TestDaemonNetworkFirewallRequiresHelper(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 19, "network.firewall", `{}`)
	if resp.Error == nil {
		t.Error("expected error when helper not running")
	}
}

func TestDaemonProcessListReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 20, "process.list", `{}`)
	if resp.Error != nil {
		t.Fatalf("process.list: %v", resp.Error)
	}
	// Result should be a JSON array (slice).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

// storage.system is deliberately not tested here: it walks ~/Library which
// can take 10+ seconds, exceeding the 5-second test socket deadline. It is
// tested via storagestate_test.go instead.

func TestDaemonStorageByAppEmptyPaths(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 21, "storage.byApp", `{"paths":[]}`)
	if resp.Error == nil {
		t.Error("expected error for empty paths")
	}
}

func TestDaemonProcessTreeReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 22, "process.tree", `{}`)
	if resp.Error != nil {
		t.Fatalf("process.tree: %v", resp.Error)
	}
	// Result should be a JSON array (slice of tree roots).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

func TestDaemonJVMThreadDumpMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 30, "jvm.thread_dump", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMHeapHistogramMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 31, "jvm.heap_histogram", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMGCStatsMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 32, "jvm.gc_stats", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMVMMemoryMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 132, "jvm.vm_memory", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJMXStatusMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 232, "jvm.jmx.status", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJMXStartLocalMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 233, "jvm.jmx.start_local", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMFlamegraphMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 234, "jvm.flamegraph", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMFlamegraphRequiresSensitiveConfirmation(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 235, "jvm.flamegraph", `{"pid":42}`)
	if resp.Error == nil {
		t.Error("expected sensitive confirmation error")
	}
}

func TestDaemonJVMExplainMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 236, "jvm.explain", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMCamelCaseAliasesRequirePID(t *testing.T) {
	tests := []struct {
		method string
		id     int
	}{
		{method: "jvm.threadDump", id: 133},
		{method: "jvm.heapHistogram", id: 134},
		{method: "jvm.gcStats", id: 135},
		{method: "jvm.heapDump", id: 136},
		{method: "jvm.vmMemory", id: 137},
		{method: "jvm.jmx.startLocal", id: 138},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			enc, dec, cancel := testDaemon(t)
			defer cancel()
			resp := rpcCall(t, enc, dec, tt.id, tt.method, `{}`)
			if resp.Error == nil {
				t.Error("expected error when pid missing")
			}
		})
	}
}

func TestDaemonJVMJFRStartMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 33, "jvm.jfr.start", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJFRDumpMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 35, "jvm.jfr.dump", `{"dest":"/tmp/out.jfr"}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJFRDumpMissingDest(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 36, "jvm.jfr.dump", `{"pid":42}`)
	if resp.Error == nil {
		t.Error("expected error when dest missing")
	}
}

func TestDaemonJVMJFRDumpRequiresSensitiveConfirmation(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 137, "jvm.jfr.dump", `{"pid":42,"dest":"/tmp/out.jfr"}`)
	if resp.Error == nil {
		t.Error("expected error when confirm_sensitive is missing")
	}
}

func TestDaemonJVMJFRDumpUsesDefaultName(t *testing.T) {
	var gotPID int
	var gotName, gotDest string
	orig := runJFRDump
	runJFRDump = func(pid int, name, dest string, _ jvm.CmdRunner) error {
		gotPID = pid
		gotName = name
		gotDest = dest
		return nil
	}
	t.Cleanup(func() { runJFRDump = orig })

	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 37, "jvm.jfr.dump", `{"pid":42,"dest":"/tmp/out.jfr","confirm_sensitive":true}`)
	if resp.Error != nil {
		t.Fatalf("jvm.jfr.dump: %v", resp.Error)
	}
	if gotPID != 42 || gotName != "spectra" || gotDest != "/tmp/out.jfr" {
		t.Fatalf("dump args = (%d, %q, %q), want (42, spectra, /tmp/out.jfr)", gotPID, gotName, gotDest)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["dumped"] != true {
		t.Errorf("dumped = %v, want true", m["dumped"])
	}
}

func TestDaemonJVMJFRStopMissingPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 34, "jvm.jfr.stop", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonJVMJFRSummaryMissingPath(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 38, "jvm.jfr.summary", `{}`)
	if resp.Error == nil {
		t.Error("expected error when path missing")
	}
}

func TestDaemonJVMJFRSummaryUsesRunner(t *testing.T) {
	var gotPath string
	orig := runJFRSummary
	runJFRSummary = func(path string, _ jvm.CmdRunner) (jvm.JFRSummary, error) {
		gotPath = path
		return jvm.JFRSummary{
			Path:    path,
			Version: "2.1",
			Events:  []jvm.JFREventSummary{{Type: "jdk.CPULoad", Count: 2, SizeBytes: 64}},
		}, nil
	}
	t.Cleanup(func() { runJFRSummary = orig })

	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 39, "jvm.jfr.summary", `{"path":"/tmp/out.jfr"}`)
	if resp.Error != nil {
		t.Fatalf("jvm.jfr.summary: %v", resp.Error)
	}
	if gotPath != "/tmp/out.jfr" {
		t.Fatalf("path = %q, want /tmp/out.jfr", gotPath)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["version"] != "2.1" {
		t.Errorf("version = %v, want 2.1", m["version"])
	}
}

func TestDaemonCacheStatsReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 40, "cache.stats", `{}`)
	if resp.Error != nil {
		t.Fatalf("cache.stats: %v", resp.Error)
	}
	// Result should be a slice (may be empty if no cache kinds registered in tests).
	if _, ok := resp.Result.([]any); !ok {
		t.Fatalf("result type %T, want []any", resp.Result)
	}
}

func TestDaemonCacheClearAll(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 41, "cache.clear", `{}`)
	if resp.Error != nil {
		t.Fatalf("cache.clear (all): %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if m["cleared"] != "all" {
		t.Errorf("cleared = %v, want all", m["cleared"])
	}
}

func TestDaemonCacheClearUnknownKindReturnsError(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// "detect" kind is not registered in the test registry (no initCacheStores),
	// so clearing it should return an RPC error.
	resp := rpcCall(t, enc, dec, 42, "cache.clear", `{"kind":"detect"}`)
	if resp.Error == nil {
		t.Error("expected error for unregistered cache kind")
	}
}

func TestDaemonJVMInspectRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 50, "jvm.inspect", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonNetworkByAppReturnsMap(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 51, "network.byApp", `{}`)
	if resp.Error != nil {
		t.Fatalf("network.byApp: %v", resp.Error)
	}
	// Result should be a map (may be empty if no connections, but must be a map).
	if _, ok := resp.Result.(map[string]any); !ok {
		t.Fatalf("network.byApp: result type %T, want map", resp.Result)
	}
}

func TestDaemonJVMHeapDumpRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 52, "jvm.heap_dump", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonJVMHeapDumpRequiresSensitiveConfirmation(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 152, "jvm.heap_dump", `{"pid":42}`)
	if resp.Error == nil {
		t.Error("expected error when confirm_sensitive is missing")
	}
}

func TestDaemonJVMHeapDumpRecordsArtifact(t *testing.T) {
	orig := runHeapDump
	var gotPID int
	var gotDest string
	runHeapDump = func(pid int, dest string, _ jvm.CmdRunner) error {
		gotPID = pid
		gotDest = dest
		return os.WriteFile(dest, []byte("heap"), 0o600)
	}
	t.Cleanup(func() { runHeapDump = orig })

	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fake := &artifact.FakeRecorder{}
	d := rpc.NewDispatcher()
	registerHandlers(d, "test-version", db, metrics.NewCollector(), livehistory.NewRing(livehistory.DefaultCapacity), cache.Default, nil, fake, nil, nil)
	client, server := net.Pipe()
	defer client.Close()
	go d.Serve(server)
	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)

	dest := filepath.Join(t.TempDir(), "heap.hprof")
	resp := rpcCall(t, enc, dec, 1, "jvm.heap_dump", `{"pid":42,"dest":"`+dest+`","confirm_sensitive":true}`)
	if resp.Error != nil {
		t.Fatalf("jvm.heap_dump: %v", resp.Error)
	}
	if gotPID != 42 || gotDest != dest {
		t.Fatalf("heap dump args = (%d, %q), want (42, %q)", gotPID, gotDest, dest)
	}
	records := fake.Snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Kind != artifact.KindHeapDump || records[0].Sensitivity != artifact.SensitivityVeryHigh {
		t.Fatalf("record = %#v", records[0])
	}
	if records[0].Path != dest || records[0].PID != 42 || records[0].CacheKind != cache.KindHprof {
		t.Fatalf("record target = %#v", records[0])
	}
}

func TestDaemonProcessSampleRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 53, "process.sample", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid=0")
	}
}

func TestDaemonHelperHealthWhenUnavailable(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// Helper is not running in tests; should return ok=false, not an error.
	resp := rpcCall(t, enc, dec, 54, "helper.health", `{}`)
	if resp.Error != nil {
		t.Fatalf("helper.health: unexpected RPC error: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("helper.health: result type %T, want map", resp.Result)
	}
	if m["helper"] != false {
		t.Errorf("helper.health: helper field = %v, want false", m["helper"])
	}
}

func TestDaemonHelperPowermetricsRequiresHelper(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	// Helper not running → should return an error (not a silent no-op).
	resp := rpcCall(t, enc, dec, 55, "helper.powermetrics.sample", `{}`)
	if resp.Error == nil {
		t.Error("expected error when helper not running")
	}
}

func TestDaemonHelperFirewallRulesRequiresHelper(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 155, "helper.firewall.rules", `{}`)
	if resp.Error == nil {
		t.Error("expected error when helper not running")
	}
}

func TestDaemonHelperFSUsageStartRequiresPID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 156, "helper.fs_usage.start", `{}`)
	if resp.Error == nil {
		t.Error("expected error when pid missing")
	}
}

func TestDaemonHelperFSUsageStopRequiresHandle(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 157, "helper.fs_usage.stop", `{}`)
	if resp.Error == nil {
		t.Error("expected error when handle missing")
	}
}

func TestDaemonHelperNetCaptureStartRequiresInterface(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 158, "helper.net_capture.start", `{}`)
	if resp.Error == nil {
		t.Error("expected error when interface missing")
	}
}

func TestDaemonHelperNetCaptureStopRequiresHandle(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 159, "helper.net_capture.stop", `{}`)
	if resp.Error == nil {
		t.Error("expected error when handle missing")
	}
}

func TestDaemonHelperNetCaptureStartRequiresHelper(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 160, "helper.net_capture.start", `{"interface":"en0"}`)
	if resp.Error == nil {
		t.Error("expected error when helper not running")
	}
}

func TestDaemonHelperTCCRequiresBundleID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 56, "helper.tcc.system.query", `{}`)
	if resp.Error == nil {
		t.Error("expected error when bundle_id missing")
	}
}

func TestDaemonHelperTCCRejectsInvalidBundleID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 156, "helper.tcc.system.query", `{"bundle_id":"x'; --"}`)
	if resp.Error == nil {
		t.Fatal("expected error when bundle_id is invalid")
	}
}

func TestDaemonSnapshotProcessesRequiresID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 57, "snapshot.processes", `{}`)
	if resp.Error == nil {
		t.Error("expected error when id missing")
	}
}

func TestDaemonToolchainBrewReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 58, "toolchain.brew", `{}`)
	if resp.Error != nil {
		t.Fatalf("toolchain.brew: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	// Should have formulae, casks, taps keys (even if empty slices / nil).
	for _, key := range []string{"formulae", "casks", "taps"} {
		if _, exists := m[key]; !exists {
			t.Errorf("toolchain.brew: missing key %q", key)
		}
	}
}

func TestDaemonToolchainRuntimesReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 59, "toolchain.runtimes", `{}`)
	if resp.Error != nil {
		t.Fatalf("toolchain.runtimes: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	for _, key := range []string{"node", "python", "go", "ruby", "rust"} {
		if _, exists := m[key]; !exists {
			t.Errorf("toolchain.runtimes: missing key %q", key)
		}
	}
}

func TestDaemonPowerStateReturnsObject(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 60, "power.state", `{}`)
	if resp.Error != nil {
		t.Fatalf("power.state: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T, want map", resp.Result)
	}
	if _, exists := m["on_battery"]; !exists {
		t.Error("power.state: expected on_battery field in result")
	}
}

func TestDaemonIssuesAcknowledgeMissingID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 61, "issues.acknowledge", `{}`)
	if resp.Error == nil {
		t.Fatal("expected error for missing id, got nil")
	}
}

func TestDaemonIssuesDismissMissingID(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 62, "issues.dismiss", `{}`)
	if resp.Error == nil {
		t.Fatal("expected error for missing id, got nil")
	}
}

func TestDaemonToolchainBuildToolsReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 63, "toolchain.build_tools", `{}`)
	if resp.Error != nil {
		t.Fatalf("toolchain.build_tools: %v", resp.Error)
	}
	// Result should be a JSON array (may be empty if no build tools installed).
	if _, ok := resp.Result.([]any); !ok && resp.Result != nil {
		t.Fatalf("result type %T, want []any or nil", resp.Result)
	}
}

func TestDaemonJDKScanReturnsSlice(t *testing.T) {
	enc, dec, cancel := testDaemon(t)
	defer cancel()
	resp := rpcCall(t, enc, dec, 64, "jdk.scan", `{}`)
	if resp.Error != nil {
		t.Fatalf("jdk.scan: %v", resp.Error)
	}
	if _, ok := resp.Result.([]any); !ok && resp.Result != nil {
		t.Fatalf("result type %T, want []any or nil", resp.Result)
	}
}
