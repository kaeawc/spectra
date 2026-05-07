package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolDefinitionsExposeWorkflowSurface(t *testing.T) {
	got := map[string]bool{}
	for _, def := range toolDefinitions() {
		got[def.Name] = true
	}
	for _, name := range []string{
		"triage",
		"inspect_app",
		"snapshot",
		"diagnose",
		"process",
		"jvm",
		"network",
		"toolchain",
		"issues",
		"remote",
	} {
		if !got[name] {
			t.Fatalf("missing tool definition %q", name)
		}
	}
}

func TestInspectAppRequiresPaths(t *testing.T) {
	s := NewServer(strings.NewReader(""), &strings.Builder{})
	result := s.toolInspectApp(json.RawMessage(`{}`))
	if !result.IsError {
		t.Fatal("expected inspect_app without paths to fail")
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "paths is required") {
		t.Fatalf("unexpected error content: %+v", result.Content)
	}
}

func TestRemoteHealthDefaultsToTCP(t *testing.T) {
	schema := operationToolDef("remote", "remote", []string{"health"})
	raw, err := json.Marshal(schema.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "operation") {
		t.Fatalf("remote schema does not expose operation: %s", raw)
	}
}
