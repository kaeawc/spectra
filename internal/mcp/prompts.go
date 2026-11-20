package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func promptDefinitions() []*sdk.Prompt {
	return []*sdk.Prompt{
		{
			Name:        "incident-triage",
			Description: "Triage an app, PID, host, or symptom.",
			Arguments: []*sdk.PromptArgument{
				{Name: "target", Description: "App path, PID, bundle ID, host, or symptom.", Required: true},
			},
		},
		{
			Name:        "jvm-memory-debug",
			Description: "Debug JVM heap, GC, and native memory.",
			Arguments: []*sdk.PromptArgument{
				{Name: "pid", Description: "JVM PID.", Required: true},
			},
		},
		{
			Name:        "baseline-diff-review",
			Description: "Review changes between two snapshots.",
			Arguments: []*sdk.PromptArgument{
				{Name: "id_a", Description: "Baseline snapshot ID.", Required: true},
				{Name: "id_b", Description: "New snapshot ID.", Required: true},
			},
		},
	}
}

// promptInstructions holds each prompt's instruction text. The SDK rejects
// names that were never registered, so every promptDefinitions entry needs
// one here.
var promptInstructions = map[string]string{
	"incident-triage":      "Start with triage. Then use diagnose, process, network, jvm, or toolchain only when the evidence points there.",
	"jvm-memory-debug":     "Use jvm inspect, gc_stats, vm_memory, and explain first. Request heap_dump or flamegraph only with confirm_sensitive=true.",
	"baseline-diff-review": "Use snapshot operation=diff. Summarize changed apps, processes, JVMs, network, and toolchains. Give next actions.",
}

func getPrompt(_ context.Context, req *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
	message, ok := promptInstructions[req.Params.Name]
	if !ok {
		return nil, fmt.Errorf("unknown prompt: %s", req.Params.Name)
	}
	return buildPromptResult(message, req.Params.Arguments), nil
}

func buildPromptResult(message string, args map[string]string) *sdk.GetPromptResult {
	raw, _ := json.MarshalIndent(args, "", "  ")
	return &sdk.GetPromptResult{
		Description: message,
		Messages: []*sdk.PromptMessage{{
			Role:    "user",
			Content: &sdk.TextContent{Text: message + "\n\nContext:\n" + string(raw)},
		}},
	}
}
