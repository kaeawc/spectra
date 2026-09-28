# MCP tools

`spectra-mcp` serves local diagnostic tools over stdio. It does not start the
daemon. Start the local service with `spectra daemon start` or install it with
`spectra daemon install` to use these daemon-backed tools:

| Tool | Parameters | Result |
| --- | --- | --- |
| `host_health` | `samples` (0–240, optional) | Current host sample, active alerts, and optional recent samples |
| `alerts` | `action` (`list` or `ack`), `state` (`firing`, `resolved`, `all`), `limit`, `id` for ack | Alert list or acknowledged alert |
| `process` | `operation: "history"`, `pid`, optional `limit` | Recent metrics for the PID |

The `host_health` and `alerts` tools return a structured tool error with a
startup hint when the daemon is unavailable. The `process` history operation
retains its unavailable error with the same hint. Daemon calls have a two-second
timeout and reconnect once after a broken connection. The existing MCP tools
using in-process collectors remain available without the daemon.

The `spectra://alerts/firing` resource returns the current firing alerts as
JSON and also requires the daemon.
