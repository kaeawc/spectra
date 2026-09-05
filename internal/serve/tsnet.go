package serve

import (
	"context"
	"net"
	"net/netip"

	"tailscale.com/tsnet"
)

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

// TsnetFactory constructs a tsnet node from Spectra's normalized config.
// Tests inject fakes so unit tests never enroll a real tailnet node.
type TsnetFactory func(TsnetConfig) MeshNode

// NewTsnetServer returns the production embedded Tailscale node.
func NewTsnetServer(cfg TsnetConfig) MeshNode {
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

func (n *realTsnetNode) Addrs() []netip.Addr {
	ip4, ip6 := n.server.TailscaleIPs()
	out := make([]netip.Addr, 0, 2)
	if ip4.IsValid() {
		out = append(out, ip4)
	}
	if ip6.IsValid() {
		out = append(out, ip6)
	}
	return out
}

func (n *realTsnetNode) WhoIs(ctx context.Context, remoteAddr string) (*MeshPeer, error) {
	lc, err := n.server.LocalClient()
	if err != nil {
		return nil, err
	}
	who, err := lc.WhoIs(ctx, remoteAddr)
	if err != nil {
		return nil, err
	}
	peer := &MeshPeer{}
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
