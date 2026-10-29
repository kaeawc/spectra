# Network failure

Use this when an app cannot reach a service, connects to the wrong host,
is affected by VPN/proxy/DNS state, or appears to be moving unexpected
traffic.

## Start with the machine

Capture the local network state before focusing on one app:

```bash
spectra network
spectra network --json
```

Look for the default route, active VPN, DNS resolvers, proxy settings,
listening ports, and per-process throughput.

## Focus on the app

Diagnose a running app, PID, or explicit endpoint:

```bash
spectra network diagnose --app /Applications/Slack.app
spectra network diagnose --pid <pid>
spectra network diagnose --app /Applications/Slack.app --ports 443 api.example.com
```

Use static endpoint extraction when the question is "what hosts does this
bundle reference?" rather than "what sockets are live right now?":

```bash
spectra -v --network /Applications/Slack.app
```

## Read the result

| Signal | Next step |
|---|---|
| DNS fails or resolves differently than expected | Compare `scutil --dns` context and explicit probe host |
| TCP connect fails | Check route, VPN, firewall, and endpoint port |
| TLS connects but issuer/subject is unexpected | Inspect proxy or interception policy |
| App has no live sockets | Confirm the app is exercising the failing workflow |
| Static hosts differ from live sockets | Separate bundled configuration from runtime behavior |

## Compromised CA in the presented chain

`spectra network diagnose` compares every certificate in a presented TLS chain
(leaf, intermediates, and root) against a table of CAs whose keys are publicly
known to be compromised. A match produces a `high` finding and
`compromised_ca=YES` on the port line; JSON output sets `compromised_ca` and
`compromised_ca_detail` on the TLS probe and on the matching chain entry. The
check runs whether or not the chain validates against the system trust store,
because a host that still trusts an affected root would otherwise validate a
forged chain silently.

The finding means the presented chain includes an affected CA. It does not
mean the host is compromised. Fix: update the OS and browser trust stores, and
reissue the certificate from an unaffected CA.

The table is `internal/netdiag/affected_cas.json`, embedded at build time. Each
entry has a name, a certificate SHA-256 (`cert_sha256`, hex) and/or an SPKI pin
(`spki_pin`, base64, same format as `leaf_spki_pin`), a role (`root` or
`intermediate`), a primary-source URL, and the as-of date of that source. To
update it, copy values only from a primary source (for example the Mozilla
Bugzilla incident bug, CCADB, or a Chrome Root Program notice); never derive or
approximate a fingerprint. Entries without a verified value are left out.

The current entries are the 16 eMudhra (emSign) G3B/G3C intermediates, with
their Sectigo cross-signs, disclosed in
[Mozilla bug 2083449](https://bugzilla.mozilla.org/show_bug.cgi?id=2083449).
The distrusted root, `emSign ECC Root CA - G3`, is not in the table because no
primary source gave its fingerprint. A standalone check of whether the host
trust store still contains that root is not implemented.

## Capture bounded packet evidence

Use the privileged helper only for explicit, bounded captures. Keep filters
narrow enough to avoid collecting unrelated traffic:

```bash
spectra network capture start --interface en0 --duration 30s --proto tcp --host api.example.com --port 443
spectra network capture stop --summarize netcap-1
spectra network capture summarize --json /var/tmp/spectra-netcap/501/netcap-1.pcap
```

Check firewall rules when local packet policy is in scope:

```bash
spectra network firewall
spectra network firewall --json
```

## Repeatable local comparison

Network failures are often time-specific. Capture a baseline before reproducing
the failure, then compare it with the current local state:

```bash
spectra snapshot --baseline before-network-failure
spectra network
spectra diff baseline before-network-failure live
```

## References

- [Network endpoints](../inspection/network-endpoints.md)
- [Live data sources](../inspection/live-data-sources.md)
- [CLI commands](../operations/cli.md)
