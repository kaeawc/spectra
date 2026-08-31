package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeNebulaCert(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("cert-"+name), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeNebulaCertPrint returns a nebula-cert-shaped JSON payload keyed on the
// cert filename so tests can control each cert's overlay IP independently.
func fakeNebulaCertPrint(t *testing.T, byBase map[string]string, isCa map[string]bool) func(string) ([]byte, error) {
	t.Helper()
	return func(path string) ([]byte, error) {
		base := filepath.Base(path)
		ip, ok := byBase[base]
		if !ok {
			return nil, fmt.Errorf("no fake cert for %s", base)
		}
		payload := map[string]any{
			"details": map[string]any{
				"isCa": isCa[base],
				"ips":  []string{ip},
			},
		}
		return json.Marshal(payload)
	}
}

func TestDiscoverNebulaPeersFromCertDir(t *testing.T) {
	dir := t.TempDir()
	writeNebulaCert(t, dir, "your-mac.crt")
	writeNebulaCert(t, dir, "work-mac.crt")
	writeNebulaCert(t, dir, "ca.crt")    // skipped by name
	writeNebulaCert(t, dir, "notes.txt") // skipped: wrong suffix
	writeNebulaCert(t, dir, "lighthouse.crt")

	t.Setenv("SPECTRA_NEBULA_CERTS", dir)
	orig := runNebulaCertPrint
	runNebulaCertPrint = fakeNebulaCertPrint(t,
		map[string]string{
			"your-mac.crt":   "192.168.100.10/24",
			"work-mac.crt":   "192.168.100.11/24",
			"lighthouse.crt": "192.168.100.1/24",
		},
		nil,
	)
	t.Cleanup(func() { runNebulaCertPrint = orig })

	peers, err := discoverNebulaPeers()
	if err != nil {
		t.Fatalf("discoverNebulaPeers = %v", err)
	}
	want := []string{"192.168.100.1", "192.168.100.10", "192.168.100.11"}
	if !reflect.DeepEqual(peers, want) {
		t.Fatalf("peers = %v, want %v", peers, want)
	}
}

func TestDiscoverNebulaPeersSkipsCAAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	writeNebulaCert(t, dir, "self-ca.crt")
	writeNebulaCert(t, dir, "broken.crt")
	writeNebulaCert(t, dir, "work-mac.crt")

	t.Setenv("SPECTRA_NEBULA_CERTS", dir)
	orig := runNebulaCertPrint
	runNebulaCertPrint = func(path string) ([]byte, error) {
		switch filepath.Base(path) {
		case "self-ca.crt":
			return json.Marshal(map[string]any{"details": map[string]any{"isCa": true, "ips": []string{"192.168.100.1/24"}}})
		case "broken.crt":
			return nil, fmt.Errorf("boom")
		case "work-mac.crt":
			return json.Marshal(map[string]any{"details": map[string]any{"ips": []string{"192.168.100.11/24"}}})
		}
		return nil, fmt.Errorf("unexpected %s", path)
	}
	t.Cleanup(func() { runNebulaCertPrint = orig })

	peers, err := discoverNebulaPeers()
	if err != nil {
		t.Fatalf("discoverNebulaPeers = %v", err)
	}
	want := []string{"192.168.100.11"}
	if !reflect.DeepEqual(peers, want) {
		t.Fatalf("peers = %v, want %v", peers, want)
	}
}

func TestDiscoverNebulaPeersMissingDir(t *testing.T) {
	t.Setenv("SPECTRA_NEBULA_CERTS", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := discoverNebulaPeers(); err == nil {
		t.Fatal("expected error for missing cert dir")
	}
}

func TestSelectDiscoverProvider(t *testing.T) {
	orig := fanDiscoverPeers
	t.Cleanup(func() { fanDiscoverPeers = orig })

	cases := []struct {
		via  string
		want func() ([]string, error)
	}{
		{"nebula", discoverNebulaPeers},
		{"tailscale", discoverTailscalePeers},
		{"", discoverAllPeers},
		{"auto", discoverAllPeers},
		{"both", discoverAllPeers},
	}
	for _, tc := range cases {
		if err := selectDiscoverProvider(tc.via); err != nil {
			t.Fatalf("%q: %v", tc.via, err)
		}
		if reflect.ValueOf(fanDiscoverPeers).Pointer() != reflect.ValueOf(tc.want).Pointer() {
			t.Fatalf("via %q selected the wrong provider", tc.via)
		}
	}

	if err := selectDiscoverProvider("wireguard"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestDiscoverAllPeersMergesSources(t *testing.T) {
	dir := t.TempDir()
	writeNebulaCert(t, dir, "work-mac.crt")
	t.Setenv("SPECTRA_NEBULA_CERTS", dir)

	origCert := runNebulaCertPrint
	runNebulaCertPrint = fakeNebulaCertPrint(t,
		map[string]string{"work-mac.crt": "192.168.100.11/24"}, nil)
	t.Cleanup(func() { runNebulaCertPrint = origCert })

	origTS := runTailscaleStatus
	runTailscaleStatus = func() ([]byte, error) {
		return json.Marshal(map[string]any{
			"Peers": map[string]map[string]string{
				"a": {"DNSName": "alice.tailnet.example"},
			},
		})
	}
	t.Cleanup(func() { runTailscaleStatus = origTS })

	peers, err := discoverAllPeers()
	if err != nil {
		t.Fatalf("discoverAllPeers = %v", err)
	}
	want := []string{"192.168.100.11", "alice.tailnet.example"}
	if !reflect.DeepEqual(peers, want) {
		t.Fatalf("peers = %v, want %v", peers, want)
	}
}

func TestDiscoverAllPeersToleratesOneFailedSource(t *testing.T) {
	// No Nebula certs on disk (dir missing) — that source errors, but Tailscale
	// still yields a peer, so discoverAllPeers succeeds.
	t.Setenv("SPECTRA_NEBULA_CERTS", filepath.Join(t.TempDir(), "absent"))

	origTS := runTailscaleStatus
	runTailscaleStatus = func() ([]byte, error) {
		return json.Marshal(map[string]any{
			"Peers": map[string]map[string]string{
				"a": {"HostName": "work-mac"},
			},
		})
	}
	t.Cleanup(func() { runTailscaleStatus = origTS })

	peers, err := discoverAllPeers()
	if err != nil {
		t.Fatalf("discoverAllPeers = %v", err)
	}
	if !reflect.DeepEqual(peers, []string{"work-mac"}) {
		t.Fatalf("peers = %v, want [work-mac]", peers)
	}
}

func TestDiscoverAllPeersErrorsWhenAllSourcesFail(t *testing.T) {
	t.Setenv("SPECTRA_NEBULA_CERTS", filepath.Join(t.TempDir(), "absent"))

	origTS := runTailscaleStatus
	runTailscaleStatus = func() ([]byte, error) { return nil, fmt.Errorf("tailscale missing") }
	t.Cleanup(func() { runTailscaleStatus = origTS })

	if _, err := discoverAllPeers(); err == nil {
		t.Fatal("expected error when every source fails")
	}
}
