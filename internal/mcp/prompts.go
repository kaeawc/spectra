package mcp

import (
	"encoding/json"
	"fmt"
)

func promptDefinitions() []PromptDefinition {
	return []PromptDefinition{
		{
			Name:        "incident-triage",
			Description: "Triage an app, PID, host, or symptom.",
			Arguments: []PromptArgument{
				{Name: "target", Description: "App path, PID, bundle ID, host, or symptom.", Required: true},
			},
		},
		{
			Name:        "jvm-memory-debug",
			Description: "Debug JVM heap, GC, and native memory.",
			Arguments: []PromptArgument{
				{Name: "pid", Description: "JVM PID.", Required: true},
			},
		},
		{
			Name:        "baseline-diff-review",
			Description: "Review changes between two snapshots.",
			Arguments: []PromptArgument{
				{Name: "id_a", Description: "Baseline snapshot ID.", Required: true},
				{Name: "id_b", Description: "New snapshot ID.", Required: true},
			},
		},
	}
}

func (s *Server) getPrompt(name string, args map[string]string) (PromptGetResult, *RPCError) {
	switch name {
	case "incident-triage":
		return buildPromptResult("Start with triage. Then use diagnose, process, network, jvm, or toolchain only when the evidence points there.", args)
	case "jvm-memory-debug":
		return buildPromptResult("Use jvm inspect, gc_stats, vm_memory, and explain first. Request heap_dump or flamegraph only with confirm_sensitive=true.", args)
	case "baseline-diff-review":
		return buildPromptResult("Use snapshot operation=diff. Summarize changed apps, processes, JVMs, network, and toolchains. Give next actions.", args)
	default:
		return PromptGetResult{}, &RPCError{
			Code:    -32602,
			Message: fmt.Sprintf("unknown prompt: %s", name),
		}
	}
}

func buildPromptResult(message string, args map[string]string) (PromptGetResult, *RPCError) {
	raw, _ := json.MarshalIndent(args, "", "  ")
	return PromptGetResult{
		Description: message,
		Messages: []PromptMessage{
			{
				Role:    "user",
				Content: ContentBlock{Type: "text", Text: message + "\n\nContext:\n" + string(raw)},
			},
		},
	}, nil
}
