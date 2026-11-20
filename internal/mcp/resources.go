package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	firingAlertsURI = "spectra://alerts/firing"
	hostHealthURI   = "spectra://host/health"
)

func resourceDefinitions() []*sdk.Resource {
	return []*sdk.Resource{
		{URI: firingAlertsURI, Name: "Firing Alerts", Description: "Current daemon alerts in JSON.", MIMEType: "application/json"},
		{URI: hostHealthURI, Name: "Host Health", Description: "Compact host load, memory pressure and thermal state in JSON.", MIMEType: "application/json"},
		{
			URI:         "spectra://capabilities",
			Name:        "Capabilities",
			Description: "Tool list and when to use each one.",
			MIMEType:    "text/markdown",
		},
		{
			URI:         "spectra://workflows/debugging",
			Name:        "Debug Workflows",
			Description: "Short app, JVM, network, and baseline workflows.",
			MIMEType:    "text/markdown",
		},
	}
}

// resourceHandler returns the read handler for a registered resource URI.
func (s *Server) resourceHandler(uri string) sdk.ResourceHandler {
	switch uri {
	case firingAlertsURI:
		return s.readFiringAlerts
	case hostHealthURI:
		return s.readHostHealth
	}
	return readStaticResource
}

// readStaticResource serves the markdown resources. The SDK only routes URIs
// that were registered, so an unknown URI never reaches it.
func readStaticResource(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	uri := req.Params.URI
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
		URI:      uri,
		MIMEType: "text/markdown",
		Text:     readResourceText(uri),
	}}}, nil
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
`,
	}
	return resources[uri]
}
