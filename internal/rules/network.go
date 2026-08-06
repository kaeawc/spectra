package rules

import (
	"fmt"
	"net"
	"strings"

	"github.com/kaeawc/spectra/internal/snapshot"
)

// ruleNetworkProxyConfigured surfaces an active system web proxy. A forgotten
// proxy is a common root cause of "TLS issuer is unexpected" or connectivity
// failures — the network-failure playbook lists it as a manual signal, so the
// engine flags it automatically (informational, not a defect).
func ruleNetworkProxyConfigured() Rule {
	return Rule{
		ID:       "network.proxy_configured",
		Severity: SeverityInfo,
		MatchFn:  matchNetworkProxyConfigured,
	}
}

func matchNetworkProxyConfigured(s snapshot.Snapshot) []Finding {
	p := s.Network.Proxy
	var parts []string
	if p.HTTP != "" {
		parts = append(parts, "http="+p.HTTP)
	}
	if p.HTTPS != "" {
		parts = append(parts, "https="+p.HTTPS)
	}
	if p.SOCKS != "" {
		parts = append(parts, "socks="+p.SOCKS)
	}
	if len(parts) == 0 {
		return nil
	}
	return []Finding{{
		RuleID:   "network.proxy_configured",
		Severity: SeverityInfo,
		Message:  fmt.Sprintf("A system proxy is configured (%s). Proxy interception can explain unexpected TLS issuers or blocked endpoints.", strings.Join(parts, ", ")),
		Fix:      "Confirm the proxy is intended for this network; clear it if it is stale.",
	}}
}

// ruleNetworkHostsOverride surfaces /etc/hosts entries that pin a real
// hostname to a non-loopback address, which silently overrides DNS and can
// redirect or break connectivity. The default macOS loopback/broadcast entries
// are ignored.
func ruleNetworkHostsOverride() Rule {
	return Rule{
		ID:       "network.hosts_override",
		Severity: SeverityInfo,
		MatchFn:  matchNetworkHostsOverride,
	}
}

func matchNetworkHostsOverride(s snapshot.Snapshot) []Finding {
	var findings []Finding
	for _, entry := range s.Network.HostsOverrides {
		if isDefaultHostsIP(entry.IP) {
			continue
		}
		names := realHostNames(entry.Names)
		if len(names) == 0 {
			continue
		}
		findings = append(findings, Finding{
			RuleID:   "network.hosts_override",
			Severity: SeverityInfo,
			Message:  fmt.Sprintf("/etc/hosts pins %s to %s, overriding DNS. This can silently redirect or break connectivity.", strings.Join(names, ", "), entry.IP),
			Subject:  entry.IP,
			Fix:      "Remove the /etc/hosts entry if it is stale or unintended.",
		})
	}
	return findings
}

func isDefaultHostsIP(ip string) bool {
	if ip == "" || ip == "255.255.255.255" {
		return true
	}
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

func realHostNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		switch strings.ToLower(strings.TrimSpace(n)) {
		case "", "localhost", "broadcasthost":
			continue
		}
		out = append(out, n)
	}
	return out
}
