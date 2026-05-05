# Security Policy

## Supported Versions

Spectra is pre-1.0. Security fixes target the `main` branch until the
first tagged release establishes versioned support windows.

## Reporting a Vulnerability

Please report suspected vulnerabilities privately by emailing
Jason Pearson at <jason.d.pearson@gmail.com>. Do not open a public
GitHub issue for security-sensitive reports.

Include the affected command or component, the host macOS version,
steps to reproduce, expected impact, and any logs or sample bundles
needed to validate the issue.

## Response

Expect an acknowledgement within 7 days. Confirmed vulnerabilities are
handled on a private branch until a fix is ready to publish. Release
notes and advisories should avoid exposing exploit details before users
have a reasonable upgrade path.

## Scope

Security reports are especially relevant for:

- privileged helper installation and IPC boundaries
- JSON-RPC request parsing and authorization
- local snapshot storage permissions
- handling of app bundles, process data, and user-controlled paths
- remote access over Tailscale or future network transports

Out-of-scope reports include social engineering, denial-of-service
against development-only commands, and issues that require already
compromised local administrator access unless they cross a Spectra
privilege boundary.
