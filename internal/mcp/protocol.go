// Package mcp implements the Spectra MCP server on the official MCP Go SDK.
package mcp

// ToolDefinition is one tool's name, description and JSON Schema input,
// registered with the SDK server as-is.
type ToolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ToolResult is what a Spectra tool handler returns; it is converted to the
// SDK's CallToolResult at the protocol boundary.
type ToolResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one text content item of a tool result.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
