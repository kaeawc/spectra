package rules

import (
	"os"
	"path/filepath"
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

// hostsFileEntries parses an /etc/hosts fixture the way netstate does,
// without its per-OS default filtering, so the rule alone must be correct.
func hostsFileEntries(t *testing.T, path string) []netstate.HostsEntry {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []netstate.HostsEntry
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		out = append(out, netstate.HostsEntry{IP: fields[0], Names: fields[1:]})
	}
	return out
}

func TestNetworkHostsOverrideIgnoresStockDebianHosts(t *testing.T) {
	entries := hostsFileEntries(t, filepath.Join("testdata", "hosts-debian"))
	if len(entries) != 7 {
		t.Fatalf("fixture parsed to %d entries, want 7", len(entries))
	}
	if f := matchNetworkHostsOverride(netSnap(netstate.State{HostsOverrides: entries})); f != nil {
		t.Fatalf("stock Debian /etc/hosts flagged as overrides: %+v", f)
	}
}

func TestNetworkHostsOverrideFlagsRealNameOnStockIPv6Address(t *testing.T) {
	entries := hostsFileEntries(t, filepath.Join("testdata", "hosts-debian"))
	entries = append(entries,
		netstate.HostsEntry{IP: "ff02::1", Names: []string{"ip6-allnodes", "api.example.com"}},
		netstate.HostsEntry{IP: "2001:db8::5", Names: []string{"ip6-allnodes"}},
		netstate.HostsEntry{IP: "10.0.0.5", Names: []string{"corp.internal"}},
	)
	f := matchNetworkHostsOverride(netSnap(netstate.State{HostsOverrides: entries}))
	if len(f) != 3 || f[0].Subject != "ff02::1" || f[1].Subject != "2001:db8::5" || f[2].Subject != "10.0.0.5" {
		t.Fatalf("findings = %+v, want the three non-stock entries", f)
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
