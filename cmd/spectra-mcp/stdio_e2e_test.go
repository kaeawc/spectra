package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The end-to-end test drives a real spectra-mcp process over stdio, the way
// an agent host (Claude Code's .mcp.json, waaagh) launches it. Set
// SPECTRA_MCP_BIN to a built binary to test that exact artifact; otherwise
// the test binary re-executes itself as the server (see TestMain).

const childEnv = "SPECTRA_MCP_E2E_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Args = os.Args[:1]
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type stdioClient struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr *strings.Builder
	nextID int
}

func startServer(t *testing.T) *stdioClient {
	t.Helper()
	var cmd *exec.Cmd
	if bin := os.Getenv("SPECTRA_MCP_BIN"); bin != "" {
		cmd = exec.Command(bin)
	} else {
		cmd = exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), childEnv+"=1")
	}
	// No daemon, so daemon-backed tools must degrade instead of hanging.
	cmd.Env = append(cmd.Environ(), "HOME="+t.TempDir())
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &stdioClient{t: t, cmd: cmd, in: in, lines: make(chan string, 64), stderr: stderr}
	go func() {
		defer close(c.lines)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 1<<20), 1<<26)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
	}()
	t.Cleanup(func() {
		_ = in.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return c
}

func (c *stdioClient) send(method string, params any) int {
	c.t.Helper()
	c.nextID++
	msg := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		msg["params"] = params
	}
	c.write(msg)
	return c.nextID
}

func (c *stdioClient) write(msg map[string]any) {
	c.t.Helper()
	b, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write %s: %v", b, err)
	}
}

// reply waits for the response to id. Every stdout line on the way must be a
// JSON-RPC 2.0 message: anything else would corrupt the client's stream.
func (c *stdioClient) reply(id int, timeout time.Duration) map[string]any {
	c.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				c.t.Fatalf("stdout closed waiting for id %d; stderr:\n%s", id, c.stderr)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil || m["jsonrpc"] != "2.0" {
				c.t.Fatalf("non-protocol line on stdout: %q", line)
			}
			if got, ok := m["id"].(float64); ok && int(got) == id {
				return m
			}
		case <-deadline:
			c.t.Fatalf("no reply to id %d within %s; stderr:\n%s", id, timeout, c.stderr)
		}
	}
}

func (c *stdioClient) initialize() map[string]any {
	c.t.Helper()
	id := c.send("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "spectra-e2e", "version": "0"},
	})
	r := c.reply(id, 10*time.Second)
	c.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("initialize failed: %v", r)
	}
	return res
}

func (c *stdioClient) callTool(name string, args map[string]any) (map[string]any, time.Duration) {
	c.t.Helper()
	start := time.Now()
	r := c.reply(c.send("tools/call", map[string]any{"name": name, "arguments": args}), 60*time.Second)
	res, ok := r["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("tools/call %s: JSON-RPC error %v", name, r["error"])
	}
	return res, time.Since(start)
}

func resultText(res map[string]any) string {
	var b strings.Builder
	content, _ := res["content"].([]any)
	for _, item := range content {
		if m, ok := item.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

func TestStdioEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns spectra-mcp")
	}
	c := startServer(t)

	start := time.Now()
	init := c.initialize()
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("initialize took %s; agent hosts expect a fast start", d)
	}
	if info, _ := init["serverInfo"].(map[string]any); info["name"] != "spectra-mcp" {
		t.Errorf("serverInfo = %v", init["serverInfo"])
	}
	if caps, _ := init["capabilities"].(map[string]any); caps["tools"] == nil {
		t.Errorf("capabilities do not advertise tools: %v", init["capabilities"])
	}

	t.Run("tools/list", func(t *testing.T) {
		r := c.reply(c.send("tools/list", map[string]any{}), 10*time.Second)
		res, _ := r["result"].(map[string]any)
		tools, _ := res["tools"].([]any)
		names := map[string]bool{}
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			name, _ := tool["name"].(string)
			names[name] = true
			checkToolDefinition(t, name, tool)
		}
		// The host tools agent harnesses (waaagh) route their checks through.
		for _, want := range []string{"load", "process", "orphans", "owner", "idle", "lineage", "spawns", "jvm", "memory", "system", "network", "storage", "diagnose", "triage", "alerts", "host_health"} {
			if !names[want] {
				t.Errorf("tools/list is missing %q", want)
			}
		}
	})

	root := t.TempDir()
	// Tools that must answer on every supported host (macOS and Linux),
	// without a daemon, a JDK or any device attached.
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"load", map[string]any{"top": 3}},
		{"process", map[string]any{"limit": 5}},
		{"process", map[string]any{"operation": "tree"}},
		{"orphans", map[string]any{"roots": []any{root}}},
		{"owner", map[string]any{"under": []any{root}}},
		{"lineage", map[string]any{"pid": os.Getpid()}},
		{"jvm", map[string]any{"operation": "list"}},
		{"network", map[string]any{}},
		{"system", map[string]any{}},
		{"toolchain", map[string]any{}},
	} {
		t.Run(fmt.Sprintf("call %s %v", tc.name, tc.args), func(t *testing.T) {
			res, took := c.callTool(tc.name, tc.args)
			if isErr, _ := res["isError"].(bool); isErr {
				t.Fatalf("isError: %s", resultText(res))
			}
			if text := resultText(res); !json.Valid([]byte(text)) {
				t.Errorf("text content is not JSON: %.200s", text)
			}
			t.Logf("%s answered in %s", tc.name, took.Round(time.Millisecond))
		})
	}

	t.Run("daemon tools degrade without a daemon", func(t *testing.T) {
		res, took := c.callTool("alerts", map[string]any{})
		if took > 10*time.Second {
			t.Errorf("alerts took %s without a daemon", took)
		}
		if isErr, _ := res["isError"].(bool); !isErr || !strings.Contains(resultText(res), "daemon_unavailable") {
			t.Errorf("alerts without a daemon = %s", resultText(res))
		}
	})

	t.Run("unknown tool is an error, not a hang", func(t *testing.T) {
		r := c.reply(c.send("tools/call", map[string]any{"name": "no_such_tool", "arguments": map[string]any{}}), 10*time.Second)
		res, _ := r["result"].(map[string]any)
		isErr, _ := res["isError"].(bool)
		if r["error"] == nil && !isErr {
			t.Errorf("unknown tool succeeded: %v", r)
		}
	})

	t.Run("exits on stdin EOF", func(t *testing.T) {
		_ = c.in.Close()
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("exit after EOF: %v; stderr:\n%s", err, c.stderr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("spectra-mcp still running 10s after stdin EOF")
		}
		for line := range c.lines {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) != nil {
				t.Errorf("non-protocol line on stdout after EOF: %q", line)
			}
		}
	})
}

// checkToolDefinition holds every advertised tool to what MCP clients rely
// on: a name, a description, and an object input schema whose properties
// each declare a type.
func checkToolDefinition(t *testing.T, name string, tool map[string]any) {
	t.Helper()
	if name == "" {
		t.Errorf("tool without a name: %v", tool)
		return
	}
	if d, _ := tool["description"].(string); strings.TrimSpace(d) == "" {
		t.Errorf("%s: empty description", name)
	}
	schema, _ := tool["inputSchema"].(map[string]any)
	if schema["type"] != "object" {
		t.Errorf("%s: inputSchema type = %v, want object", name, schema["type"])
	}
	props, _ := schema["properties"].(map[string]any)
	for prop, raw := range props {
		p, _ := raw.(map[string]any)
		if p["type"] == nil {
			t.Errorf("%s.%s: property has no type", name, prop)
		}
	}
	required, _ := schema["required"].([]any)
	for _, r := range required {
		if _, ok := props[r.(string)]; !ok {
			t.Errorf("%s: required %q is not a property", name, r)
		}
	}
}
