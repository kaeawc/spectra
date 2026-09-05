package serve

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
)

// MeshPeer is the overlay identity attached to a remote mesh connection.
// The tsnet provider fills LoginName, DisplayName, and NodeName from a
// Tailscale WhoIs lookup; the nebula provider fills NodeName and Groups from
// the peer's signed certificate.
type MeshPeer struct {
	LoginName   string
	DisplayName string
	NodeName    string
	Groups      []string
}

// MeshNode is the embedded overlay-node surface the daemon needs. Both the
// tsnet (Tailscale) and nebula providers satisfy it; tests inject fakes so
// unit tests never enroll or start a real overlay node.
type MeshNode interface {
	Listen(network string, addr string) (net.Listener, error)
	// Addrs returns the node's overlay addresses once the node is up. An
	// empty slice means the addresses are not known yet.
	Addrs() []netip.Addr
	// WhoIs resolves the overlay identity behind a remote address. A nil
	// peer with nil error means the overlay attested nothing for the
	// address.
	WhoIs(ctx context.Context, remoteAddr string) (*MeshPeer, error)
	Close() error
}

// MeshStatus is the health payload for an embedded overlay listener,
// published under the provider's name ("tsnet", "nebula").
type MeshStatus struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	ListenAddr string `json:"listen_addr,omitempty"`
	IPv4       string `json:"ipv4,omitempty"`
	IPv6       string `json:"ipv6,omitempty"`
}

// meshRuntime pairs a live overlay node with the status it reports.
type meshRuntime struct {
	node   MeshNode
	status *MeshStatus
}

// fillAddrs copies the node's current overlay addresses into a status copy.
func (m meshRuntime) fillAddrs(status *MeshStatus) {
	if m.node == nil {
		return
	}
	for _, addr := range m.node.Addrs() {
		switch {
		case addr.Is4() && status.IPv4 == "":
			status.IPv4 = addr.String()
		case addr.Is6() && status.IPv6 == "":
			status.IPv6 = addr.String()
		}
	}
}

// meshPolicy is the provider-independent allowlist evaluated against the
// identity a MeshNode attests for a peer. An empty policy admits every peer
// the overlay itself already authenticated.
type meshPolicy struct {
	AllowedLogins []string
	AllowedNodes  []string
	AllowedGroups []string
}

func (p meshPolicy) Enabled() bool {
	return len(p.AllowedLogins) > 0 || len(p.AllowedNodes) > 0 || len(p.AllowedGroups) > 0
}

func (p meshPolicy) Allows(peer *MeshPeer) bool {
	if peer == nil {
		return false
	}
	login := normalizeMeshLogin(peer.LoginName)
	for _, allowed := range p.AllowedLogins {
		if login != "" && login == normalizeMeshLogin(allowed) {
			return true
		}
	}
	node := normalizeMeshNode(peer.NodeName)
	for _, allowed := range p.AllowedNodes {
		if node != "" && node == normalizeMeshNode(allowed) {
			return true
		}
	}
	return p.allowsGroup(peer.Groups)
}

func (p meshPolicy) allowsGroup(groups []string) bool {
	for _, group := range groups {
		g := normalizeMeshLogin(group)
		if g == "" {
			continue
		}
		for _, allowed := range p.AllowedGroups {
			if g == normalizeMeshLogin(allowed) {
				return true
			}
		}
	}
	return false
}

// meshIdentityListener gates accepted overlay connections on the peer
// identity the node attests. With no policy configured it stays log-only:
// every peer the overlay authenticated is admitted and its identity logged.
type meshIdentityListener struct {
	net.Listener
	node     MeshNode
	provider string
	log      logger.Logger
	policy   meshPolicy
}

func (l *meshIdentityListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		remoteAddr := conn.RemoteAddr().String()
		if !l.policy.Enabled() {
			go logMeshPeer(l.node, l.provider, l.log, remoteAddr)
			return conn, nil
		}
		peer, err := lookupMeshPeer(l.node, remoteAddr)
		if err != nil {
			l.log.Warn("daemon "+l.provider+" peer rejected", "remote_addr", remoteAddr, "error", err.Error())
			_ = conn.Close()
			continue
		}
		if peer == nil {
			l.log.Warn("daemon "+l.provider+" peer rejected", "remote_addr", remoteAddr, "error", "empty identity response")
			_ = conn.Close()
			continue
		}
		if !l.policy.Allows(peer) {
			l.log.Warn(
				"daemon "+l.provider+" peer rejected",
				"remote_addr", remoteAddr,
				"login", peer.LoginName,
				"display_name", peer.DisplayName,
				"node", peer.NodeName,
				"groups", strings.Join(peer.Groups, ","),
			)
			_ = conn.Close()
			continue
		}
		logMeshPeerIdentity(l.provider, l.log, remoteAddr, peer)
		return conn, nil
	}
}

// wrapMeshIdentityListener applies the shared identity gate to a provider's
// raw overlay listener, matching the historical tsnet behavior: wrap when a
// policy is configured (to enforce it) or a logger is present (to log peer
// identities).
func wrapMeshIdentityListener(ln net.Listener, node MeshNode, provider string, log logger.Logger, hasLogger bool, policy meshPolicy) net.Listener {
	if !hasLogger && !policy.Enabled() {
		return ln
	}
	return &meshIdentityListener{
		Listener: ln,
		node:     node,
		provider: provider,
		log:      log,
		policy:   policy,
	}
}

func logMeshPeer(node MeshNode, provider string, log logger.Logger, remoteAddr string) {
	peer, err := lookupMeshPeer(node, remoteAddr)
	if err != nil {
		log.Debug("daemon "+provider+" peer identity unavailable", "remote_addr", remoteAddr, "error", err.Error())
		return
	}
	if peer == nil {
		log.Debug("daemon "+provider+" peer identity unavailable", "remote_addr", remoteAddr, "error", "empty identity response")
		return
	}
	logMeshPeerIdentity(provider, log, remoteAddr, peer)
}

func lookupMeshPeer(node MeshNode, remoteAddr string) (*MeshPeer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return node.WhoIs(ctx, remoteAddr)
}

func logMeshPeerIdentity(provider string, log logger.Logger, remoteAddr string, peer *MeshPeer) {
	log.Info(
		"daemon "+provider+" peer connected",
		"remote_addr", remoteAddr,
		"login", peer.LoginName,
		"display_name", peer.DisplayName,
		"node", peer.NodeName,
		"groups", strings.Join(peer.Groups, ","),
	)
}

func normalizeMeshLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func normalizeMeshNode(node string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(node)), ".")
}

// meshSlogHandler forwards a provider library's slog records into the daemon
// logger so embedded-node output (enrollment URLs, handshake errors) lands in
// the same JSONL stream as the rest of the daemon.
type meshSlogHandler struct {
	log      logger.Logger
	provider string
}

func (h meshSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h meshSlogHandler) Handle(_ context.Context, r slog.Record) error {
	args := make([]any, 0, 2+2*r.NumAttrs())
	args = append(args, "message", r.Message)
	r.Attrs(func(a slog.Attr) bool {
		args = append(args, a.Key, a.Value.String())
		return true
	})
	msg := "daemon " + h.provider + " status"
	switch {
	case r.Level >= slog.LevelError:
		h.log.Error(msg, args...)
	case r.Level >= slog.LevelWarn:
		h.log.Warn(msg, args...)
	default:
		h.log.Info(msg, args...)
	}
	return nil
}

func (h meshSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h meshSlogHandler) WithGroup(string) slog.Handler { return h }
