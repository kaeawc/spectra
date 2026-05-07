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

## Remote target

Network failures are often machine-specific. Compare the sick host with a
known-good host:

```bash
spectra connect work-mac network
spectra connect work-mac network-by-app /Applications/Slack.app
spectra fan --hosts work-mac,known-good call network.connections
```

## References

- [Network endpoints](../inspection/network-endpoints.md)
- [Live data sources](../inspection/live-data-sources.md)
- [CLI network commands](../operations/cli.md#spectra-network)
