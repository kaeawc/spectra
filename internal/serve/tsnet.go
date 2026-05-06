package serve

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/kaeawc/spectra/internal/logger"
	"tailscale.com/tsnet"
)

// TsnetStatus is the health payload for the embedded tailnet listener.
type TsnetStatus struct {
	Enabled    bool   `json:"enabled"`
	Hostname   string `json:"hostname,omitempty"`
	ListenAddr string `json:"listen_addr,omitempty"`
	IPv4       string `json:"ipv4,omitempty"`
	IPv6       string `json:"ipv6,omitempty"`
}

// TsnetConfig is the tsnet and Spectra-side policy configuration the daemon
// normalizes before constructing a node.
type TsnetConfig struct {
	StateDir    string
	Hostname    string
	Ephemeral   bool
	Tags        []string
	AllowLogins []string
	AllowNodes  []string
	UserLogf    func(format string, args ...any)
}

// TsnetPeer is the Tailscale identity attached to a remote tsnet connection.
type TsnetPeer struct {
	LoginName   string
	DisplayName string
	NodeName    string
}

// TsnetNode is the tsnet surface the daemon needs. Tests inject fakes so unit
// tests never enroll or start a real tailnet node.
type TsnetNode interface {
	Listen(network string, addr string) (net.Listener, error)
	TailscaleIPs() (netip.Addr, netip.Addr)
	WhoIs(ctx context.Context, remoteAddr string) (*TsnetPeer, error)
	Close() error
}

// TsnetFactory constructs a tsnet node from Spectra's normalized config.
type TsnetFactory func(TsnetConfig) TsnetNode

// NewTsnetServer returns the production embedded Tailscale node.
func NewTsnetServer(cfg TsnetConfig) TsnetNode {
	return &realTsnetNode{server: &tsnet.Server{
		Dir:           cfg.StateDir,
		Hostname:      cfg.Hostname,
		Ephemeral:     cfg.Ephemeral,
		AdvertiseTags: cfg.Tags,
		UserLogf:      cfg.UserLogf,
	}}
}

type realTsnetNode struct {
	server *tsnet.Server
}

func (n *realTsnetNode) Listen(network string, addr string) (net.Listener, error) {
	return n.server.Listen(network, addr)
}

func (n *realTsnetNode) TailscaleIPs() (netip.Addr, netip.Addr) {
	return n.server.TailscaleIPs()
}

func (n *realTsnetNode) WhoIs(ctx context.Context, remoteAddr string) (*TsnetPeer, error) {
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	peer := &TsnetPeer{}
	if who.UserProfile != nil {
		peer.LoginName = who.UserProfile.LoginName
		peer.DisplayName = who.UserProfile.DisplayName
	}
	if who.Node != nil {
		peer.NodeName = who.Node.Name
	}
	return peer, nil
}

func (n *realTsnetNode) Close() error {
	return n.server.Close()
}

type tsnetIdentityListener struct {
	net.Listener
	node   TsnetNode
	log    logger.Logger
	policy tsnetPolicy
}

func (l *tsnetIdentityListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		remoteAddr := conn.RemoteAddr().String()
		if !l.policy.Enabled() {
			go logTsnetPeer(l.node, l.log, remoteAddr)
			return conn, nil
		}
		peer, err := lookupTsnetPeer(l.node, remoteAddr)
		if err != nil {
			l.log.Warn("daemon tsnet peer rejected", "remote_addr", remoteAddr, "error", err.Error())
			_ = conn.Close()
			continue
		}
		if peer == nil {
			l.log.Warn("daemon tsnet peer rejected", "remote_addr", remoteAddr, "error", "empty whois response")
			_ = conn.Close()
			continue
		}
		if !l.policy.Allows(peer) {
			l.log.Warn(
				"daemon tsnet peer rejected",
				"remote_addr", remoteAddr,
				"login", peer.LoginName,
				"display_name", peer.DisplayName,
				"node", peer.NodeName,
			)
			_ = conn.Close()
			continue
		}
		logTsnetPeerIdentity(l.log, remoteAddr, peer)
		return conn, nil
	}
}

func logTsnetPeer(node TsnetNode, log logger.Logger, remoteAddr string) {
	peer, err := lookupTsnetPeer(node, remoteAddr)
	if err != nil {
		log.Debug("daemon tsnet peer identity unavailable", "remote_addr", remoteAddr, "error", err.Error())
		return
	}
	if peer == nil {
		log.Debug("daemon tsnet peer identity unavailable", "remote_addr", remoteAddr, "error", "empty whois response")
		return
	}
	logTsnetPeerIdentity(log, remoteAddr, peer)
}

func lookupTsnetPeer(node TsnetNode, remoteAddr string) (*TsnetPeer, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return node.WhoIs(ctx, remoteAddr)
}

func logTsnetPeerIdentity(log logger.Logger, remoteAddr string, peer *TsnetPeer) {
	log.Info(
		"daemon tsnet peer connected",
		"remote_addr", remoteAddr,
		"login", peer.LoginName,
		"display_name", peer.DisplayName,
		"node", peer.NodeName,
	)
}

type tsnetPolicy struct {
	AllowedLogins []string
	AllowedNodes  []string
}

func (p tsnetPolicy) Enabled() bool {
	return len(p.AllowedLogins) > 0 || len(p.AllowedNodes) > 0
}

func (p tsnetPolicy) Allows(peer *TsnetPeer) bool {
	if peer == nil {
		return false
	}
	login := normalizeTsnetLogin(peer.LoginName)
	node := normalizeTsnetNode(peer.NodeName)
	for _, allowed := range p.AllowedLogins {
		if login != "" && login == normalizeTsnetLogin(allowed) {
			return true
		}
	}
	for _, allowed := range p.AllowedNodes {
		if node != "" && node == normalizeTsnetNode(allowed) {
			return true
		}
	}
	return false
}

func normalizeTsnetLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func normalizeTsnetNode(node string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(node)), ".")
}
