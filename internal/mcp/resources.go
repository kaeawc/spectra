package mcp

import "fmt"

func resourceDefinitions() []ResourceDefinition {
	return []ResourceDefinition{
		{
			URI:         "spectra://capabilities",
			Name:        "Capabilities",
			Description: "Tool list and when to use each one.",
			MimeType:    "text/markdown",
		},
		{
			URI:         "spectra://workflows/debugging",
			Name:        "Debug Workflows",
			Description: "Short app, JVM, network, and baseline workflows.",
			MimeType:    "text/markdown",
		},
	}
}

func readResource(uri string) (string, string, error) {
	switch uri {
	case "spectra://capabilities":
		return readResourceText(uri), "text/markdown", nil
	case "spectra://workflows/debugging":
		return readResourceText(uri), "text/markdown", nil
	default:
		return "", "", fmt.Errorf("resource not found: %s", uri)
	}
}

func readResourceText(uri string) string {
	resources := map[string]string{
		"spectra://capabilities": `# Capabilities

- triage: start here
- inspect_app: explain one app bundle
- snapshot: create, list, get, diff
- diagnose: run rules
- process: live processes
- jvm: JVM debug
- network: routes, DNS, sockets
- toolchain: JDKs, runtimes, build tools
- issues: track findings
- remote: call a Spectra daemon

Ask:
- What looks wrong with this app?
- What changed since the baseline?
- Why is this JVM slow?
- What is this app connected to?
`,
		"spectra://workflows/debugging": `# Debug Workflows

Start with triage.

Then use:
- inspect_app for bundle facts
- process for live state
- jvm for Java/HotSpot
- network for routes, DNS, VPN, sockets
- snapshot diff for "what changed?"

Heap dumps and flamegraphs require confirm_sensitive=true.
Helper-only actions use remote operation=rpc.
`,
	}
	return resources[uri]
}
