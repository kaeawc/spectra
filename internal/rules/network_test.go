package rules

import (
	"strings"
	"testing"

	"github.com/kaeawc/spectra/internal/netstate"
	"github.com/kaeawc/spectra/internal/snapshot"
)

func netSnap(state netstate.State) snapshot.Snapshot {
	var s snapshot.Snapshot
	s.Network = state
	return s
}

func TestNetworkProxyConfiguredFires(t *testing.T) {
	s := netSnap(netstate.State{Proxy: netstate.ProxyConfig{HTTPS: "proxy.corp:8080"}})
	f := matchNetworkProxyConfigured(s)
	if len(f) != 1 || !strings.Contains(f[0].Message, "proxy.corp:8080") {
		t.Fatalf("proxy finding = %+v", f)
	}
	if none := matchNetworkProxyConfigured(netSnap(netstate.State{})); none != nil {
		t.Fatalf("expected no finding without a proxy, got %+v", none)
	}
}

func TestNetworkHostsOverrideFlagsRealHostOnly(t *testing.T) {
	s := netSnap(netstate.State{HostsOverrides: []netstate.HostsEntry{
		{IP: "127.0.0.1", Names: []string{"localhost"}},           // default, ignored
		{IP: "255.255.255.255", Names: []string{"broadcasthost"}}, // default, ignored
		{IP: "::1", Names: []string{"localhost"}},                 // loopback, ignored
		{IP: "10.0.0.5", Names: []string{"api.example.com"}},      // real override
	}})
	f := matchNetworkHostsOverride(s)
	if len(f) != 1 {
		t.Fatalf("findings = %d, want 1 (only the real override): %+v", len(f), f)
	}
	if !strings.Contains(f[0].Message, "api.example.com") || f[0].Subject != "10.0.0.5" {
		t.Fatalf("finding = %+v", f[0])
	}
}

func TestNetworkHostsOverrideIgnoresLoopbackWithRealName(t *testing.T) {
	// A real hostname pinned to loopback is still a local redirect, not a
	// DNS-hijack; the default-IP filter skips it.
	s := netSnap(netstate.State{HostsOverrides: []netstate.HostsEntry{
		{IP: "127.0.0.1", Names: []string{"dev.local"}},
	}})
	if f := matchNetworkHostsOverride(s); f != nil {
		t.Fatalf("expected no finding for loopback pin, got %+v", f)
	}
}
