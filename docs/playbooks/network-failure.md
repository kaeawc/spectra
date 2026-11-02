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
`compromised_ca_detail` on the TLS probe, and `compromised_ca` on each matching
chain entry. The
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
[Mozilla bug 2083449](https://bugzilla.mozilla.org/show_bug.cgi?id=2083449),
and the distrusted root `emSign ECC Root CA - G3`. The root's SHA-256 comes
from the [CCADB all-certificate records report](https://ccadb.my.salesforce-sites.com/ccadb/AllCertificateRecordsCSVFormatv4)
(record A005756; Mozilla status Removed, Chrome status Blocked) and agrees with
Mozilla's removed-roots report. In CCADB the G3B/G3C TLS and code-signing
intermediates are issued by `emSign Root TLS CA - G3` and `emSign Root CS CA - G3`,
whose cross-certificates are signed by `emSign ECC Root CA - G3`; the Sectigo
cross-signs chain elsewhere.

### Host trust-store check

`spectra network diagnose` also checks, once per run, whether the host trust
store still contains an affected root from the table. The finding reports
presence only; it does not mean the host is compromised, and it does not
inspect per-certificate trust settings, so a certificate present with trust
disabled is still reported. Severity and advice depend on the OS:

- On Linux the finding is `high` ("the host trust store still contains emSign
  ECC Root CA - G3") and the fix is to update the OS and browser trust stores.
- On macOS the finding is `medium`. At the time the table was last updated (the
  entry's `as_of`), CCADB listed Apple's status for this root as Included while
  Mozilla had removed it and Chrome had blocked it, so updating the trust store
  may not help yet. The finding says so, and suggests checking for a macOS
  update or adjusting the certificate's trust settings in Keychain Access.

- Linux: parses the PEM bundles `/etc/ssl/certs/ca-certificates.crt`,
  `/etc/pki/tls/certs/ca-bundle.crt`, and `/etc/ssl/cert.pem` and compares the
  SHA-256 of each certificate's DER. An unreadable bundle is skipped and
  recorded; only when no bundle can be read is the check reported as skipped.
  Only plain `CERTIFICATE` PEM blocks are parsed, not OpenSSL `TRUSTED
  CERTIFICATE` blocks. Browser-private stores (for example
  Firefox's NSS database) are not read.
- macOS: runs `security find-certificate -a -Z` against the system roots
  keychain and the default keychain search list and compares the reported
  `SHA-256 hash` values. The parser is unit-tested on sample output; it has not
  been run against a real macOS keychain.
- If no store can be read, the report carries an `info` finding saying the check
  was skipped (the check is bounded to 10 seconds); other operating systems skip the check silently.

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
