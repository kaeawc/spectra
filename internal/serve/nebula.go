package serve

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/kaeawc/spectra/internal/logger"
	"github.com/slackhq/nebula"
	nebulaconfig "github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
	"github.com/slackhq/nebula/service"
)

// NebulaConfig is the nebula provider configuration the daemon normalizes
// before constructing a node. ConfigPath points at a standard nebula
// config.yaml (pki, static_host_map, lighthouse, firewall). Spectra runs the
// node in-process with a userspace network stack, so no tun device and no
// root are required, and the overlay can reach only what the daemon listens
// on — nothing else on the machine.
type NebulaConfig struct {
	ConfigPath string
	Version    string
	Logger     logger.Logger
}

// NebulaFactory constructs a nebula node from Spectra's normalized config.
// Tests inject fakes so unit tests never join a real overlay.
type NebulaFactory func(NebulaConfig) (MeshNode, error)

// NewNebulaServer returns the production embedded Nebula node.
func NewNebulaServer(cfg NebulaConfig) (MeshNode, error) {
	var c nebulaconfig.C
	if err := c.Load(cfg.ConfigPath); err != nil {
		return nil, fmt.Errorf("load nebula config %s: %w", cfg.ConfigPath, err)
	}
	log := cfg.Logger
	if log == nil {
		log = logger.Discard()
	}
	slogger := slog.New(meshSlogHandler{log: log, provider: "nebula"})
	control, err := nebula.Main(&c, false, cfg.Version, slogger, overlay.NewUserDeviceFromConfig)
	if err != nil {
		return nil, fmt.Errorf("start nebula node: %w", err)
	}
	svc, err := service.New(control)
	if err != nil {
		return nil, fmt.Errorf("start nebula service: %w", err)
	}
	return &realNebulaNode{control: control, svc: svc}, nil
}

type realNebulaNode struct {
	control *nebula.Control
	svc     *service.Service
}

func (n *realNebulaNode) Listen(network string, addr string) (net.Listener, error) {
	return n.svc.Listen(network, addr)
}

func (n *realNebulaNode) Addrs() []netip.Addr {
	prefixes := n.control.Device().Networks()
	out := make([]netip.Addr, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix.Addr().IsValid() {
			out = append(out, prefix.Addr())
		}
	}
	return out
}

// WhoIs resolves a peer's overlay address to the identity in its signed
// certificate: the cert name and groups. Nebula already authenticated the
// cert against the mesh CA before any packet reached the daemon, so this is
// attested identity, not a claim.
func (n *realNebulaNode) WhoIs(_ context.Context, remoteAddr string) (*MeshPeer, error) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil, fmt.Errorf("parse nebula peer address %q: %w", remoteAddr, err)
	}
	info := n.control.GetHostInfoByVpnAddr(addr, false)
	if info == nil || info.Cert == nil {
		return nil, nil
	}
	return &MeshPeer{
		NodeName: info.Cert.Name(),
		Groups:   info.Cert.Groups(),
	}, nil
}

func (n *realNebulaNode) Close() error {
	return n.svc.Close()
}
