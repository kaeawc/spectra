package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// selectDiscoverProvider maps the --discover-via value to the peer source used
// by `spectra hosts` and `spectra fan`. The default, "auto", attempts every
// source and merges the results, so plain `--discover` finds peers whether the
// host is on Tailscale, a Nebula mesh, or both. Tailscale queries a live
// coordination server; Nebula has no such peer list, so its source of truth is
// the set of signed host certs (see discoverNebulaPeers).
func selectDiscoverProvider(via string) error {
	switch strings.ToLower(strings.TrimSpace(via)) {
	case "", "auto", "both", "all":
		fanDiscoverPeers = discoverAllPeers
	case "tailscale":
		fanDiscoverPeers = discoverTailscalePeers
	case "nebula":
		fanDiscoverPeers = discoverNebulaPeers
	default:
		return fmt.Errorf("unknown --discover-via %q (want auto, tailscale, or nebula)", via)
	}
	return nil
}

// discoverAllPeers merges every discovery source, deduping by target string. A
// source that fails (Tailscale not installed, no Nebula certs on disk) is
// tolerated: its error is only surfaced when *every* source failed and no peers
// were found, so a Tailscale-only or Nebula-only host still discovers cleanly.
func discoverAllPeers() ([]string, error) {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	var errs []error
	merge := func(peers []string, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		for _, peer := range peers {
			if _, ok := seen[peer]; ok {
				continue
			}
			seen[peer] = struct{}{}
			out = append(out, peer)
		}
	}
	merge(discoverTailscalePeers())
	merge(discoverNebulaPeers())
	sort.Strings(out)
	if len(out) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("discover peers: all sources failed: %w", errors.Join(errs...))
	}
	return out, nil
}

// nebulaCertsDir is the directory scanned for signed Nebula host certs.
// $SPECTRA_NEBULA_CERTS overrides the default of ~/.nebula.
func nebulaCertsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("SPECTRA_NEBULA_CERTS")); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".nebula"), nil
}

// discoverNebulaPeers enumerates signed host certs and returns their overlay
// IPs. Nebula exposes no live peer list, so cert membership is the source of
// truth: every host you can reach has a cert signed by the mesh CA. The
// --discover-daemons probe then narrows this to hosts actually running a
// Spectra daemon. Overlay IPs are returned rather than names because Nebula
// has no MagicDNS; parseConnectTarget dials a bare IP directly.
func discoverNebulaPeers() ([]string, error) {
	dir, err := nebulaCertsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read nebula certs %s: %w", dir, err)
	}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == "ca.crt" || !strings.HasSuffix(name, ".crt") {
			continue
		}
		ip, err := nebulaCertIP(filepath.Join(dir, name))
		if err != nil || ip == "" {
			// Skip unreadable or CA certs rather than failing the whole listing.
			continue
		}
		seen[ip] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for ip := range seen {
		hosts = append(hosts, ip)
	}
	sort.Strings(hosts)
	return hosts, nil
}

// nebulaCertIP returns the bare overlay IP from a signed Nebula cert, dropping
// the CIDR mask so parseConnectTarget can dial it. Empty string means the cert
// carries no usable host IP (for example a CA cert).
func nebulaCertIP(path string) (string, error) {
	raw, err := runNebulaCertPrint(path)
	if err != nil {
		return "", err
	}
	var payload struct {
		Details struct {
			IsCa bool     `json:"isCa"`
			Ips  []string `json:"ips"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if payload.Details.IsCa || len(payload.Details.Ips) == 0 {
		return "", nil
	}
	ip := strings.TrimSpace(payload.Details.Ips[0])
	if idx := strings.IndexByte(ip, '/'); idx >= 0 {
		ip = ip[:idx]
	}
	return ip, nil
}

// runNebulaCertPrint is the seam tests replace; production shells out to the
// nebula-cert binary that ships with Nebula.
var runNebulaCertPrint = func(path string) ([]byte, error) {
	return exec.Command("nebula-cert", "print", "-json", "-path", path).Output()
}
